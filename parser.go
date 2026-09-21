package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// VideoInfo 视频信息
type VideoInfo struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Duration    string `json:"duration"`
	URL         string `json:"url"`
}

// ParseResult 标准 JSON 解析结果
type ParseResult struct {
	Code  int     `json:"code"`
	Msg   string  `json:"msg"`
	Title string  `json:"title"`
	Type  string  `json:"type"`
	URL   string  `json:"url"`
	From  string  `json:"from"`
	Time  float64 `json:"time"`
}

// 移动端 UA（用于回退提取标题，部分站点 PC 端反爬严格）
const mobileUserAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Mobile/15E148 Safari/604.1"

// 常见平台移动端域名映射（标题提取回退用）
var mobileDomainMap = map[string]string{
	"www.iqiyi.com":    "m.iqiyi.com",
	"v.qq.com":         "m.v.qq.com",
	"www.youku.com":    "m.youku.com",
	"www.bilibili.com": "m.bilibili.com",
}

// VideoParser 视频解析 API 核心类（与原 Python 版解析逻辑一致）
type VideoParser struct {
	client    *http.Client
	parseAPIs []string
}

// NewVideoParser 创建解析器实例
func NewVideoParser() *VideoParser {
	return &VideoParser{
		client: &http.Client{Timeout: 15 * time.Second},
		parseAPIs: []string{
			"https://jx.playerjy.com/?url=",
			"https://jx.aidouer.net/?url=",
			"https://jx.jsonplayer.com/?url=",
			"https://jx.bozrc.com:4433/player/?url=",
		},
	}
}

// newRequest 构造带优化请求头的请求
func (p *VideoParser) newRequest(method, rawURL string) (*http.Request, error) {
	req, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://www.iqiyi.com/")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	// 注意: 不手动设置 Accept-Encoding, 由 Go 自动处理 gzip 解压
	req.Header.Set("Connection", "keep-alive")
	return req, nil
}

// get 发送 GET 请求并读取响应体（timeout 毫秒；0 表示使用默认 15s）
func (p *VideoParser) get(rawURL string, timeout time.Duration) (string, error) {
	return p.getWithUA(rawURL, timeout, "")
}

// getWithUA 发送 GET 请求，可指定 User-Agent（空则使用默认 PC UA）
func (p *VideoParser) getWithUA(rawURL string, timeout time.Duration, ua string) (string, error) {
	req, err := p.newRequest(http.MethodGet, rawURL)
	if err != nil {
		return "", err
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	resp, err := p.client.Do(req.WithContext(ctx))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// ParseVideo 解析视频并返回标准 JSON 格式
func (p *VideoParser) ParseVideo(videoURL string, showURLOnError bool) *ParseResult {
	start := time.Now()

	videoInfo, err := p.GetVideoInfo(videoURL)
	videoTitle := "未知视频"
	if err == nil && videoInfo.Title != "" {
		videoTitle = videoInfo.Title
	}

	playURLs := p.GeneratePlayURLs(videoURL)
	elapsed := time.Since(start).Seconds()

	if len(playURLs) > 0 {
		// 直链校验核心：对候选地址做真实可播校验，只返回 TVBox 能直接播放的 m3u8/mp4
		if verified := p.verifyAndResolve(videoURL, playURLs, 8*time.Second); verified != "" {
			return &ParseResult{
				Code:  200,
				Msg:   "获取成功",
				Title: videoTitle,
				Type:  p.DetectVideoType(verified),
				URL:   verified,
				From:  videoURL,
				Time:  round(elapsed),
			}
		}
		// 未找到可播直链：若首个候选是无扩展名的 HTML 播放页(如加密/JS 播放器)，
		// 则尝试用无头浏览器让页面 JS 真实执行并捕获 m3u8；仍失败才如实返回。
		first := playURLs[0]
		if !p.IsDirectMedia(first) {
			if hl := p.resolveWithHeadless(videoURL, 18*time.Second); hl != "" {
				return &ParseResult{
					Code:  200,
					Msg:   "获取成功（无头浏览器兜底）",
					Title: videoTitle,
					Type:  p.DetectVideoType(hl),
					URL:   hl,
					From:  videoURL,
					Time:  round(elapsed),
				}
			}
			return &ParseResult{
				Code:  404,
				Msg:   "未找到可直接播放的视频源（该链接可能为加密或 JS 播放器）",
				Title: videoTitle,
				Type:  "",
				URL:   videoURL,
				From:  videoURL,
				Time:  round(elapsed),
			}
		}
		return &ParseResult{
			Code:  200,
			Msg:   "获取成功",
			Title: videoTitle,
			Type:  p.DetectVideoType(first),
			URL:   first,
			From:  videoURL,
			Time:  round(elapsed),
		}
	}

	code := 404
	msg := "解析失败，未找到播放源"
	urlOut := ""
	if showURLOnError {
		urlOut = videoURL
	}
	return &ParseResult{
		Code:  code,
		Msg:   msg,
		Title: videoTitle,
		Type:  "",
		URL:   urlOut,
		From:  videoURL,
		Time:  round(elapsed),
	}
}

// GetVideoInfo 获取视频信息（标题等）。
// 优先从原始页面提取；若 PC 端页面因反爬无法提取标题，
// 自动回退到移动端域名 + 移动端 UA 再次尝试。
func (p *VideoParser) GetVideoInfo(videoURL string) (*VideoInfo, error) {
	html, err := p.get(videoURL, 15*time.Second)
	if err != nil {
		return &VideoInfo{Title: "未知视频", URL: videoURL}, err
	}

	info := extractVideoInfo(html, videoURL)
	if info.Title == "未知视频" {
		if mobile := p.tryMobileInfo(videoURL); mobile != nil && mobile.Title != "未知视频" {
			return mobile, nil
		}
	}
	return info, nil
}

// tryMobileInfo 使用移动端域名 + 移动端 UA 重试提取视频信息
func (p *VideoParser) tryMobileInfo(videoURL string) *VideoInfo {
	mobileURL := toMobileURL(videoURL)
	if mobileURL == "" || mobileURL == videoURL {
		return nil
	}
	html, err := p.getWithUA(mobileURL, 15*time.Second, mobileUserAgent)
	if err != nil {
		return nil
	}
	info := extractVideoInfo(html, videoURL) // URL 保持原链接
	if info.Title != "未知视频" {
		return info
	}
	return nil
}

// toMobileURL 将 PC 端域名转换为移动端域名
func toMobileURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if m, ok := mobileDomainMap[u.Host]; ok {
		u.Host = m
		return u.String()
	}
	return ""
}

// extractVideoInfo 从 HTML 中提取视频信息
func extractVideoInfo(html, videoURL string) *VideoInfo {
	info := &VideoInfo{Title: "", Description: "", Duration: "", URL: videoURL}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return &VideoInfo{Title: "未知视频", URL: videoURL}
	}

	// 标题 - 按优先级尝试（与原 Python 版一致）
	titleSelectors := []struct {
		selector string
		attrType string // content | text
	}{
		{`meta[property="og:title"]`, "content"},
		{`meta[name="title"]`, "content"},
		{"h1.videoTitle", "text"},
		{"h1.title", "text"},
		{`h1[class*="title"]`, "text"},
		{".video-title", "text"},
		{"title", "text"},
	}

	for _, s := range titleSelectors {
		sel := doc.Find(s.selector)
		if sel.Length() == 0 {
			continue
		}
		var title string
		if s.attrType == "content" {
			title = strings.TrimSpace(sel.AttrOr("content", ""))
		} else {
			title = strings.TrimSpace(sel.Text())
		}
		if title != "" {
			info.Title = cleanTitle(title)
			break
		}
	}
	if info.Title == "" {
		info.Title = "未知视频"
	}

	// 描述（尽力获取）
	descSelectors := []struct {
		selector string
		attrType string
	}{
		{`meta[name="description"]`, "content"},
		{`meta[property="og:description"]`, "content"},
		{".video-desc", "text"},
		{".description", "text"},
	}
	for _, s := range descSelectors {
		sel := doc.Find(s.selector)
		if sel.Length() == 0 {
			continue
		}
		var desc string
		if s.attrType == "content" {
			desc = strings.TrimSpace(sel.AttrOr("content", ""))
		} else {
			desc = strings.TrimSpace(sel.Text())
		}
		if desc != "" {
			info.Description = desc
			break
		}
	}

	return info
}

// cleanTitle 清理标题：去掉分隔符后缀与网站名称
func cleanTitle(title string) string {
	t := title
	if idx := strings.Index(t, "_"); idx >= 0 {
		t = t[:idx]
	}
	if idx := strings.Index(t, "-"); idx >= 0 {
		t = t[:idx]
	}
	t = strings.TrimSpace(t)
	for _, site := range []string{"爱奇艺", "腾讯视频", "优酷", "芒果TV", "iQIYI", "Tencent", "在线观看", "高清"} {
		t = strings.ReplaceAll(t, site, "")
	}
	return strings.TrimSpace(t)
}

// GeneratePlayURLs 生成多种可能的播放地址（与原 Python 版一致）
func (p *VideoParser) GeneratePlayURLs(videoURL string) []string {
	playURLs := make([]string, 0, 8)
	seen := make(map[string]bool)

	addUnique := func(u string) {
		if !seen[u] {
			seen[u] = true
			playURLs = append(playURLs, u)
		}
	}

	// 方法1：第三方接口解析（主要方法）
	for _, api := range p.parseAPIs {
		parseURL := api + videoURL
		html, err := p.get(parseURL, 10*time.Second)
		if err == nil {
			for _, u := range p.ExtractPlayURLs(html) {
				addUnique(u)
			}
			if len(playURLs) > 0 {
				break // 找到就停止
			}
		}
		time.Sleep(500 * time.Millisecond) // 短暂延迟
	}

	// 方法2：直接解析
	if len(playURLs) == 0 {
		for _, u := range p.TryDirectParse(videoURL) {
			addUnique(u)
		}
	}

	// 方法3：生成通用播放器链接（备用方案）
	if len(playURLs) == 0 {
		for _, api := range p.parseAPIs {
			addUnique(api + videoURL)
		}
	}

	// 最多返回 5 个播放源
	if len(playURLs) > 5 {
		playURLs = playURLs[:5]
	}
	return playURLs
}

// ExtractPlayURLs 从 HTML 中提取播放源（与原 Python 版一致）
func (p *VideoParser) ExtractPlayURLs(html string) []string {
	patterns := []string{
		`"url":"([^"]+\.m3u8[^"]*)"`,
		`"url":"([^"]+\.mp4[^"]*)"`,
		`"playUrl":"([^"]+)"`,
		`"src":"([^"]+\.m3u8[^"]*)"`,
		`"src":"([^"]+\.mp4[^"]*)"`,
		`<iframe[^>]+src="([^"]+)"`,
		`<video[^>]+src="([^"]+)"`,
		`<source[^>]+src="([^"]+)"`,
	}

	playURLs := make([]string, 0, 8)
	seen := make(map[string]bool)
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		for _, match := range re.FindAllStringSubmatch(html, -1) {
			if len(match) < 2 {
				continue
			}
			u := match[1]
			if !seen[u] && p.IsValidVideoURL(u) {
				seen[u] = true
				playURLs = append(playURLs, u)
			}
		}
	}
	return playURLs
}

// TryDirectParse 尝试直接解析页面获取播放源
func (p *VideoParser) TryDirectParse(videoURL string) []string {
	html, err := p.get(videoURL, 15*time.Second)
	if err != nil {
		return nil
	}

	patterns := []string{
		`"playUrl":"([^"]+)"`,
		`"url":"([^"]+\.m3u8[^"]*)"`,
		`"url":"([^"]+\.mp4[^"]*)"`,
		`"videoUrl":"([^"]+)"`,
		`"src":"([^"]+\.m3u8[^"]*)"`,
		`"src":"([^"]+\.mp4[^"]*)"`,
		`playUrl=([^&\s]+)`,
		`videoUrl=([^&\s]+)`,
	}

	playURLs := make([]string, 0, 8)
	seen := make(map[string]bool)
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		for _, match := range re.FindAllStringSubmatch(html, -1) {
			if len(match) < 2 {
				continue
			}
			u := match[1]
			if decoded, err := url.QueryUnescape(u); err == nil {
				u = decoded
			}
			if !seen[u] {
				seen[u] = true
				playURLs = append(playURLs, u)
			}
		}
	}
	return playURLs
}

// IsDirectMedia 判断地址是否直接指向媒体资源(m3u8/mp4/flv 等)，而非 HTML 播放页
func (p *VideoParser) IsDirectMedia(u string) bool {
	if u == "" {
		return false
	}
	lower := strings.ToLower(u)
	for _, ext := range []string{".m3u8", ".mp4", ".flv", ".ts", ".m3u"} {
		if strings.Contains(lower, ext) {
			return true
		}
	}
	return false
}

// IsValidVideoURL 验证是否为有效视频 URL
func (p *VideoParser) IsValidVideoURL(videoURL string) bool {
	if videoURL == "" || len(videoURL) < 10 {
		return false
	}
	lower := strings.ToLower(videoURL)
	for _, ext := range []string{".m3u8", ".mp4", ".flv", ".avi", ".mkv", ".mov", ".wmv"} {
		if strings.Contains(lower, ext) {
			return true
		}
	}
	for _, kw := range []string{"video", "player", "play", "stream", "media"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// DetectVideoType 检测视频类型
func (p *VideoParser) DetectVideoType(videoURL string) string {
	lower := strings.ToLower(videoURL)
	switch {
	case strings.Contains(lower, ".m3u8"):
		return "m3u8"
	case strings.Contains(lower, ".mp4"):
		return "mp4"
	case strings.Contains(lower, ".flv"):
		return "flv"
	default:
		return "m3u8" // 默认返回 m3u8
	}
}

// round 保留 6 位小数
func round(v float64) float64 {
	return float64(int64(v*1e6+0.5)) / 1e6
}

// formatParseError 构造解析异常结果
func formatParseError(videoURL string, showURLOnError bool, err error, elapsed float64) *ParseResult {
	urlOut := ""
	if showURLOnError {
		urlOut = videoURL
	}
	return &ParseResult{
		Code:  500,
		Msg:   fmt.Sprintf("解析异常: %v", err),
		Title: "",
		Type:  "",
		URL:   urlOut,
		From:  videoURL,
		Time:  round(elapsed),
	}
}
