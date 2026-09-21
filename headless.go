package main

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// headless.go
// 无头浏览器兜底层：当纯直链解析失败（加密/JS 播放器源）时，用无头 Chromium
// 让页面自身的 JS/WebSocket 真实执行，再用网络层拦截捕获页面动态请求出的真实 m3u8 地址。
//
// 浏览器资源与程序解耦：
//   - 用户自行下载 Chromium 资源包，解压部署到程序同目录的 browser/ 目录（详见 browser/README.md）；
//   - 更新浏览器只需直接替换 browser/ 目录下的资源，程序与浏览器互不影响；
//   - 程序不自动下载浏览器；找不到本地浏览器时兜底自动跳过，不影响普通解析。
//
// 性能优化：浏览器进程常驻复用，全局只启动一次；每次解析仅新建/关闭页面（tab），
// 不再反复启停浏览器进程，显著降低调用耗时。

// headlessEnabled 是否启用无头浏览器兜底。默认启用；可用 PYXT_HEADLESS=0 关闭。
func headlessEnabled() bool {
	return os.Getenv("PYXT_HEADLESS") != "0"
}

// 常驻浏览器实例（全局单例，进程内复用；浏览器进程异常退出后自动重建）
var (
	headlessMu   sync.Mutex
	headlessInst *rod.Browser
	headlessL    *launcher.Launcher
)

// findChromeBin 定位 Chromium 可执行文件：
// 优先 CHROME_BIN 环境变量 → 程序目录下 browser/ 浏览器资源目录 → 系统 PATH。
// 浏览器资源包由用户自行下载部署，程序不再自动下载。
func findChromeBin() string {
	// 1. 环境变量显式指定
	if b := os.Getenv("CHROME_BIN"); b != "" {
		if _, err := os.Stat(b); err == nil {
			return b
		}
	}
	// 2. browser/ 浏览器资源目录（用户自行部署，直接替换即可更新浏览器）
	if b := findChromeInBrowserDir("browser"); b != "" {
		return b
	}
	// 3. 系统 PATH 中已安装的 chrome/chromium
	if p, ok := launcher.LookPath(); ok {
		return p
	}
	return ""
}

// findChromeInBrowserDir 在浏览器资源目录中递归查找 Chromium 可执行文件。
// 目录结构由用户自定义（如 browser/chrome-linux64/chrome、browser/chrome.exe 等），
// 按常见可执行文件名匹配，最多向下查找 5 层避免扫描过深。
func findChromeInBrowserDir(dir string) string {
	names := []string{
		"chrome", "chromium", "headless_shell", "chrome-headless-shell",
		"chrome.exe", "chromium.exe", "msedge",
	}
	const maxDepth = 5
	var found string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil || strings.Count(rel, string(filepath.Separator)) >= maxDepth {
			return nil
		}
		for _, n := range names {
			if strings.EqualFold(d.Name(), n) {
				if st, serr := os.Stat(path); serr == nil && !st.IsDir() {
					found = path
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	return found
}

// ensureBrowser 获取可用的无头浏览器实例：首次调用时启动浏览器进程并缓存，
// 后续调用直接复用；若浏览器进程异常退出，下次调用自动重建。并发安全。
func ensureBrowser() (*rod.Browser, error) {
	headlessMu.Lock()
	defer headlessMu.Unlock()

	if headlessInst != nil {
		if _, err := headlessInst.Version(); err == nil {
			return headlessInst, nil // 复用常驻实例
		}
		// 连接已失效（浏览器进程被杀等），清理后重建
		_ = headlessInst.Close()
		headlessInst = nil
	}

	binPath := findChromeBin()
	if binPath == "" {
		return nil, fmt.Errorf("未找到 Chromium：请自行下载浏览器资源包并部署到 browser/ 目录（详见 browser/README.md），或通过 CHROME_BIN 环境变量指定路径")
	}

	l := launcher.New().
		Bin(binPath).
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
		return nil, fmt.Errorf("启动浏览器失败: %w", err)
	}

	b := rod.New().ControlURL(launchURL)
	if err := b.Connect(); err != nil {
		l.Cleanup()
		return nil, fmt.Errorf("连接浏览器失败: %w", err)
	}

	headlessL = l
	headlessInst = b
	log.Printf("[headless] 无头浏览器已启动并常驻复用: %s", binPath)
	return b, nil
}

// closeHeadlessBrowser 关闭常驻浏览器实例（程序退出时调用，幂等）。
func closeHeadlessBrowser() {
	headlessMu.Lock()
	defer headlessMu.Unlock()
	if headlessInst != nil {
		_ = headlessInst.Close()
		headlessInst = nil
	}
	if headlessL != nil {
		headlessL.Cleanup()
		headlessL = nil
	}
}

// resolveWithHeadless 用无头浏览器解析加密源，返回第一个捕获到的 m3u8/mp4 直链。
// 复用常驻浏览器实例，每次解析仅新建/关闭页面；多个解析源依次尝试，命中即返回。
func (p *VideoParser) resolveWithHeadless(videoURL string, budget time.Duration) string {
	if !headlessEnabled() {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	browser, err := ensureBrowser()
	if err != nil {
		log.Printf("[headless] 浏览器不可用，跳过兜底: %v", err)
		return ""
	}

	// 网络层拦截：捕获请求/响应中的 m3u8/mp4 地址（本次解析独立状态，互不串扰）
	var mu sync.Mutex
	var mediaURL string
	record := func(u string) {
		if u == "" {
			return
		}
		lower := strings.ToLower(u)
		if !strings.Contains(lower, ".m3u8") && !strings.Contains(lower, ".mp4") {
			return
		}
		mu.Lock()
		if mediaURL == "" {
			mediaURL = u
		}
		mu.Unlock()
	}

	router := browser.HijackRequests()
	_ = router.Add("*", proto.NetworkResourceType(""), func(h *rod.Hijack) {
		record(h.Request.URL().String())
		h.ContinueRequest(&proto.FetchContinueRequest{})
	})
	defer router.Stop()
	go router.Run()

	// 依次走各解析接口，让页面+iframe 的 JS/WebSocket 真实执行
	for _, api := range p.parseAPIs {
		if ctx.Err() != nil {
			break
		}
		target := api + videoURL
		page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
		if err != nil {
			continue
		}
		_ = page.SetUserAgent(&proto.NetworkSetUserAgentOverride{
			UserAgent: directUA, AcceptLanguage: "zh-CN,zh;q=0.9",
		})
		if err := page.Navigate(target); err != nil {
			_ = page.Close()
			continue
		}
		// 等待命中介质地址（页面链可能耗时数秒）
		if p.waitMedia(ctx, &mu, &mediaURL) {
			mu.Lock()
			res := mediaURL
			mu.Unlock()
			_ = page.Close()
			return res
		}
		_ = page.Close()
	}

	mu.Lock()
	res := mediaURL
	mu.Unlock()
	return res
}

// waitMedia 轮询是否捕获到媒体地址，直到命中或上下文超时。
func (p *VideoParser) waitMedia(ctx context.Context, mu *sync.Mutex, mediaURL *string) bool {
	tick := 300 * time.Millisecond
	total := 7 * time.Second // 单个源最多等这么久
	start := time.Now()
	for {
		if ctx.Err() != nil {
			return false
		}
		mu.Lock()
		hit := *mediaURL != ""
		mu.Unlock()
		if hit {
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
