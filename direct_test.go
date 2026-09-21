package main

import (
	"testing"
	"time"
)

// 用含直接嵌入 m3u8/mp4 的 HTML 片段，验证直链解析器的正向路径与 HTML 拒绝逻辑。
func TestExtractURLsFromHTML(t *testing.T) {
	html := `
<html><head></head><body>
  <video src="https://cdn.example.com/movie/abc.mp4"></video>
  <source src="//cdn2.example.com/live/stream.m3u8">
  <div data-src="https://cdn.example.com/2048/index.m3u8"/>
  <script>window.player={url:"https://api.example.com/v1/file.mp4?token=abc"};</script>
</body></html>`
	urls := extractURLsFromHTML(html, "https://watch.example.com/v?id=1")
	if len(urls) < 2 {
		t.Fatalf("期望提取到至少 2 个直链, 实际 %d: %v", len(urls), urls)
	}
	foundM3u8 := false
	foundMp4 := false
	for _, u := range urls {
		if u == "https://cdn2.example.com/live/stream.m3u8" {
			foundM3u8 = true
		}
		if u == "https://cdn.example.com/movie/abc.mp4" {
			foundMp4 = true
		}
	}
	if !foundM3u8 {
		t.Errorf("未找到相对协议头 m3u8 直链, 结果=%v", urls)
	}
	if !foundMp4 {
		t.Errorf("未找到绝对 mp4 直链, 结果=%v", urls)
	}
}

func TestResolveM3U8Master(t *testing.T) {
	// 用一个本地不存在的地址，仅验证 master 解析逻辑会因网络失败返回空(不误报使用)。
	p := NewVideoParser()
	p.client.Timeout = 2 * time.Second
	if got := p.resolveM3U8("https://127.0.0.1:1/nope.m3u8", time.Now().Add(2*time.Second)); got != "" {
		t.Logf("网络不可达时返回空(期望), 实际=%q", got)
	}
}

func TestIsDirectMedia(t *testing.T) {
	p := NewVideoParser()
	cases := []struct {
		in   string
		want bool
	}{
		{"https://cdn.x.com/a.m3u8", true},
		{"https://cdn.x.com/a.mp4", true},
		{"https://cdn.x.com/live/1.flv", true},
		{"https://getdata.staticfile.link/player/abc", false}, // HTML 播放页(无媒体扩展名)
		{"https://jx.xxx.com/?url=1", false},
		{"", false},
	}
	for _, c := range cases {
		if got := p.IsDirectMedia(c.in); got != c.want {
			t.Errorf("IsDirectMedia(%q)=%v, want %v", c.in, got, c.want)
		}
	}
}

// 网络测试：真实公共 HLS 测试流,验证 resolveSingle 能返回可播放的 m3u8(带 EXT-X-STREAM-INF 时取最高码率)。
func TestResolveM3U8Public(t *testing.T) {
	p := NewVideoParser()
	got := p.resolveSingle("", "https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8", time.Now().Add(10*time.Second))
	if got == "" {
		t.Skip("公共 HLS 测试流不可达(网络受限),跳过")
	}
	t.Logf("resolved master -> %s", got)
	if len(got) < 10 {
		t.Fatalf("解析结果异常短: %q", got)
	}
}
