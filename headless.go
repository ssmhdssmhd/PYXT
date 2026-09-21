package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// headless.go
// 无头浏览器兜底层：当纯直链解析失败（加密/JS 播放器源）时，启动一个无头 Chromium，
// 让页面自身的 JS/WebSocket 真实执行，再用网络层拦截捕获页面动态请求出的真实 m3u8。
//
// 性能优化要点：
//   - 浏览器复用：Chromium 进程启动一次后持续复用，不再"每个请求都拉起+关闭"（消除秒级开销）；
//   - 浏览器资源包外置：用户把 Chromium/Chrome 部署到 BROWSER_DIR（默认 ./browser）目录即可，
//     程序优先从该目录定位。浏览器更新只需替换目录，程序更新只换程序，两者彻底分离、互不影响；
//   - 空闲自动回收：长时间无人调用时自动关闭浏览器释放内存，下次调用自动重新拉起。
//
// 正确性说明：为避免页面级拦截漏掉跨域 iframe(OOPIF) 的请求，这里采用"浏览器级拦截"，
// 与浏览器复用配套，无头解析用互斥锁串行执行，确保同一时刻只有一个捕获目标，不互相串台。

// headlessEnabled 是否启用无头浏览器兜底。默认启用；可用 PYXT_HEADLESS=0 关闭。
func headlessEnabled() bool {
	return os.Getenv("PYXT_HEADLESS") != "0"
}

// browserDir 浏览器资源包目录。用户自行下载部署 Chromium/Chrome 到该目录即可，
// 程序优先从该目录定位浏览器，避免运行时联网下载。默认 ./browser，可用 BROWSER_DIR 覆盖。
func browserDir() string {
	if d := os.Getenv("BROWSER_DIR"); d != "" {
		return d
	}
	return "./browser"
}

// idleTimeout 浏览器空闲回收时长。默认 180 秒，可用 PYXT_BROWSER_IDLE 覆盖（单位：秒）。
func idleTimeout() time.Duration {
	if v := os.Getenv("PYXT_BROWSER_IDLE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 180 * time.Second
}

// 浏览器路径解析结果只计算一次（避免每次调用都遍历磁盘或联网下载）。
var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// findChromeBin 定位 Chromium 可执行文件（仅解析一次并缓存结果）。
// 优先级：浏览器目录(browser/) → CHROME_BIN → 系统常见安装路径 → 联网自动下载一次。
func findChromeBin() string {
	binOnce.Do(func() { binPath, binErr = locateBrowserBin() })
	if binErr != nil || binPath == "" {
		return ""
	}
	return binPath
}

func locateBrowserBin() (string, error) {
	if b := os.Getenv("CHROME_BIN"); b != "" {
		if _, err := os.Stat(b); err == nil {
			return b, nil
		}
	}
	if b, err := findInBrowserDir(); err == nil {
		return b, nil
	}
	if p, ok := launcher.LookPath(); ok {
		return p, nil
	}
	return launcher.NewBrowser().MustGet(), nil
}

// findInBrowserDir 在用户部署的浏览器资源包目录中查找 Chromium/Chrome 可执行文件。
// 支持放在该目录或其一层的子目录，文件名以 chrome/chromium/msedge/google-chrome 开头即视为候选。
func findInBrowserDir() (string, error) {
	root := browserDir()
	fi, err := os.Stat(root)
	if err != nil || !fi.IsDir() {
		return "", os.ErrNotExist
	}
	var cand string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		base := strings.ToLower(d.Name())
		for _, pre := range []string{"chrome", "chromium", "msedge", "google-chrome"} {
			if strings.HasPrefix(base, pre) {
				cand = path
				return filepath.SkipAll
			}
		}
		return nil
	})
	if cand != "" {
		return cand, nil
	}
	return "", os.ErrNotExist
}

// captureState 单次解析的网络捕获状态，记录页面动态请求出的第一个 m3u8/mp4 地址。
type captureState struct {
	mu  sync.Mutex
	url string
}

func (c *captureState) record(u string) {
	if u == "" {
		return
	}
	lower := strings.ToLower(u)
	if !strings.Contains(lower, ".m3u8") && !strings.Contains(lower, ".mp4") {
		return
	}
	c.mu.Lock()
	if c.url == "" {
		c.url = u
	}
	c.mu.Unlock()
}

func (c *captureState) get() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.url
}

// headlessManager 管理一个"持续复用"的无头浏览器实例，并串行化无头解析。
type headlessManager struct {
	mu       sync.Mutex
	browser  *rod.Browser
	launcher *launcher.Launcher
	router   *rod.HijackRouter
	idle     *time.Timer
	lastUsed time.Time
	current  *captureState // 当前正在捕获的解析（浏览器级拦截写入它）

	parseMu sync.Mutex // 串行化无头解析，保证浏览器级拦截不互相串台
}

var hls = &headlessManager{}

// ensureBrowser 返回可用的浏览器实例；若无则惰性启动一个并建立浏览器级拦截与空闲回收定时器。
// 返回的浏览器由管理器统一管理（持续复用），调用方不得自行关闭。
func (m *headlessManager) ensureBrowser() *rod.Browser {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.browser != nil {
		m.touchLocked()
		return m.browser
	}
	bin := findChromeBin()
	if bin == "" {
		return nil
	}
	l := launcher.New().
		Bin(bin).
		Headless(true).
		NoSandbox(true).
		Set("--disable-gpu").
		Set("--disable-dev-shm-usage").
		Set("--no-first-run").
		Set("--disable-extensions").
		Set("--disable-background-networking").
		Set("--mute-audio")
	launchURL, err := l.Launch()
	if err != nil {
		l.Cleanup()
		return nil
	}
	browser := rod.New().ControlURL(launchURL)
	if err := browser.Connect(); err != nil {
		l.Cleanup()
		return nil
	}

	// 浏览器级拦截：捕获所有响应/请求中的 m3u8/mp4（含跨域 iframe），写入当前解析的捕获状态。
	router := browser.HijackRequests()
	_ = router.Add("*", proto.NetworkResourceType(""), func(h *rod.Hijack) {
		m.mu.Lock()
		cs := m.current
		m.mu.Unlock()
		if cs != nil {
			cs.record(h.Request.URL().String())
		}
		h.ContinueRequest(&proto.FetchContinueRequest{})
	})
	go router.Run()

	m.browser = browser
	m.launcher = l
	m.router = router
	m.touchLocked()
	log.Printf("[headless] 浏览器已就绪并进入复用: %s", bin)
	return browser
}

// warm 刷新"最近使用时间"，让浏览器在较长的解析过程中保持常驻不被空闲回收。
func (m *headlessManager) warm() {
	m.mu.Lock()
	m.touchLocked()
	m.mu.Unlock()
}

func (m *headlessManager) touchLocked() {
	m.lastUsed = time.Now()
	m.scheduleIdleLocked()
}

func (m *headlessManager) scheduleIdleLocked() {
	if m.idle != nil {
		m.idle.Stop()
	}
	m.idle = time.AfterFunc(idleTimeout(), m.closeIdle)
}

// closeIdle 浏览器空闲超时后关闭以释放内存；若最近刚被复用则重新计时。
func (m *headlessManager) closeIdle() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.browser == nil {
		return
	}
	if time.Since(m.lastUsed) < idleTimeout() {
		m.scheduleIdleLocked()
		return
	}
	m.teardownLocked()
}

// resetBrowser 强制回收当前浏览器（连接失效需重建前调用）。
func (m *headlessManager) resetBrowser() {
	m.mu.Lock()
	m.teardownLocked()
	m.mu.Unlock()
}

// teardownLocked 关闭当前浏览器并清空状态（需持有锁）。
func (m *headlessManager) teardownLocked() {
	b := m.browser
	l := m.launcher
	r := m.router
	m.browser = nil
	m.launcher = nil
	m.router = nil
	m.current = nil
	if m.idle != nil {
		m.idle.Stop()
		m.idle = nil
	}
	if r != nil {
		_ = r.Stop()
	}
	if b != nil {
		_ = b.Close()
	}
	if l != nil {
		l.Cleanup()
	}
}

// resolveWithHeadless 用无头浏览器解析加密源，返回捕获到的 m3u8/mp4 直链。
// 浏览器进程被持续复用（headlessManager），且无头解析串行执行：
// 相比"每个请求重新拉起浏览器"要快得多，同时又不会因复用而让浏览器级拦截串台。
func (p *VideoParser) resolveWithHeadless(videoURL string, budget time.Duration) string {
	if !headlessEnabled() {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	hls.parseMu.Lock()
	defer hls.parseMu.Unlock()

	browser := hls.ensureBrowser()
	if browser == nil {
		return ""
	}

	// 注册当前捕获目标（串行保证同一时刻只有一个，浏览器级拦截只会写入它）。
	cs := &captureState{}
	hls.mu.Lock()
	hls.current = cs
	hls.mu.Unlock()
	defer func() {
		hls.mu.Lock()
		if hls.current == cs {
			hls.current = nil
		}
		hls.mu.Unlock()
	}()
	hls.warm()

	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		// 连接可能已失效：回收后重建一次再试
		hls.resetBrowser()
		browser = hls.ensureBrowser()
		if browser == nil {
			return ""
		}
		page, err = browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
		if err != nil {
			return ""
		}
	}
	defer func() { _ = page.Close() }()

	_ = page.SetUserAgent(&proto.NetworkSetUserAgentOverride{
		UserAgent: directUA, AcceptLanguage: "zh-CN,zh;q=0.9",
	})

	// 依次走各解析接口，让页面+iframe 的 JS/WebSocket 真实执行
	for _, api := range p.parseAPIs {
		if ctx.Err() != nil {
			break
		}
		target := api + videoURL
		if err := page.Navigate(target); err != nil {
			continue
		}
		if p.waitMedia(ctx, cs) {
			return cs.get()
		}
	}
	return cs.get()
}

// waitMedia 轮询是否捕获到媒体地址，直到命中、单个源超时或整体上下文结束。
func (p *VideoParser) waitMedia(ctx context.Context, cs *captureState) bool {
	tick := 300 * time.Millisecond
	total := 7 * time.Second // 单个源最多等这么久
	start := time.Now()
	for {
		if ctx.Err() != nil {
			return false
		}
		if cs.get() != "" {
			return true
		}
		if time.Since(start) >= total {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(tick):
		}
	}
}