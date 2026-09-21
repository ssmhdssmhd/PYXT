package main

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// headless.go
// 无头浏览器兜底层：当纯直链解析失败（加密/JS 播放器源）时，临时拉起一个无头 Chromium，
// 让页面自身的 JS/WebSocket 真实执行，再用网络层拦截捕获页面动态请求出的真实 m3u8 地址。
// 只在需要时才启用，用完即销毁。

// headlessEnabled 是否启用无头浏览器兜底。默认启用；可用 PYXT_HEADLESS=0 关闭。
func headlessEnabled() bool {
	return os.Getenv("PYXT_HEADLESS") != "0"
}

// findChromeBin 定位 Chromium 可执行文件：
// 优先 CHROME_BIN 环境变量 → 常见安装路径 → 自动下载到 ~/.cache/rod/browser
func findChromeBin() string {
	if b := os.Getenv("CHROME_BIN"); b != "" {
		if _, err := os.Stat(b); err == nil {
			return b
		}
	}
	if p, ok := launcher.LookPath(); ok {
		return p
	}
	return launcher.NewBrowser().MustGet()
}

// resolveWithHeadless 用无头浏览器解析加密源，返回第一个捕获到的 m3u8/mp4 直链。
func (p *VideoParser) resolveWithHeadless(videoURL string, budget time.Duration) string {
	if !headlessEnabled() {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	binPath := findChromeBin()
	if binPath == "" {
		return ""
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
	defer l.Cleanup()

	launchURL, err := l.Launch()
	if err != nil {
		return ""
	}

	browser := rod.New().ControlURL(launchURL)
	if err := browser.Connect(); err != nil {
		return ""
	}
	defer browser.MustClose()

	// ---- 网络层拦截：捕获响应/请求中的 m3u8/mp4 地址 ----
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
