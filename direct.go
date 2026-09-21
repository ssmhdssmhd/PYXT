package main

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// direct.go
// 直链解析核心：对第三方接口/页面提取到的候选地址做"真实可播校验"，
// 只返回能被 TVBox/通用播放器直接播放的 m3u8 或 mp4 直链（而非 HTML 播放页）。

// 校验用请求头：按视频网站常规请求携带，避免被当成简单爬虫拒绝。
var directUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

var (
	m3u8ExtRe   = regexp.MustCompile(`(?i)\.m3u8(?:[?#]|$)`)
	mp4ExtRe    = regexp.MustCompile(`(?i)\.mp4(?:[?#]|$)`)
	masterInfRe = regexp.MustCompile(`#EXT-X-STREAM-INF:[^\r\n]*BANDWIDTH=(\d+)`)
)

// verifyAndResolve 依次校验候选地址，返回第一个"真实可播"的直链。
// 不带协议头的补全 http；m3u8 master 列表自动选码率最高子流。
// totalTimeout 为整体预算，避免拖慢接口。
func (p *VideoParser) verifyAndResolve(videoURL string, candidates []string, totalTimeout time.Duration) string {
	deadline := time.Now().Add(totalTimeout)
	for _, c := range candidates {
		if time.Now().After(deadline) {
			break
		}
		resolved := p.resolveSingle(videoURL, c, deadline)
		if resolved == "" {
			continue
		}
		if m3u8ExtRe.MatchString(resolved) {
			// m3u8 已在 resolveM3U8 内验证为 #EXTM3U 列表，直接可用
			return resolved
		}
		// mp4 等：确认地址连通且非 404/HTML
		if p.isReachableMedia(resolved, deadline) {
			return resolved
		}
	}
	return ""
}

// resolveSingle 判断单个候选并解析为可播直链。
func (p *VideoParser) resolveSingle(videoURL, candidate string, deadline time.Time) string {
	u := strings.TrimSpace(candidate)
	u = normalizeCandidateURL(u)
	if u == "" {
		return ""
	}

	switch {
	case m3u8ExtRe.MatchString(u):
		return p.resolveM3U8(u, deadline)
	case mp4ExtRe.MatchString(u):
		return u
	default:
		// 可能是 HTML 播放页 / iframe，最多再深挖 2 层
		return p.followNested(videoURL, u, deadline, 2)
	}
}

// resolveM3U8 抓取 m3u8 内容，若是 master 列表则挑选码率最高的子流地址（支持相对路径）。
func (p *VideoParser) resolveM3U8(raw string, deadline time.Time) string {
	body, err := p.getUntil(raw, deadline)
	if err != nil || body == "" {
		return ""
	}
	if !strings.HasPrefix(strings.TrimSpace(body), "#EXTM3U") {
		return "" // 不是真正的 m3u8 列表
	}

	m := masterInfRe.FindAllStringSubmatch(body, -1)
	if len(m) == 0 {
		return raw // 单个媒体播放列表，直接可用
	}

	// master：挑选 BANDWIDTH 最大的子流
	bestURL, bestBW := raw, int64(-1)
	lines := strings.Split(body, "\n")
	for i, ln := range lines {
		if masterInfRe.MatchString(ln) {
			// 取紧随其后的非注释、非空行作为子流地址（支持 #EXT-X-STREAM-INF 下一行）
			for j := i + 1; j < len(lines); j++ {
				v := strings.TrimSpace(lines[j])
				if v == "" || strings.HasPrefix(v, "#") {
					continue
				}
				bw := parseBandwidth(ln)
				if bw > bestBW {
					bestBW = bw
					bestURL = resolveRelativeRef(raw, v)
				}
				break
			}
		}
	}
	return bestURL
}

// followNested 对 HTML/iframe 类地址递归提取真实 m3u8/mp4，深度最深 deep 层。
func (p *VideoParser) followNested(videoURL, raw string, deadline time.Time, deep int) string {
	if deep <= 0 || time.Now().After(deadline) {
		return ""
	}
	body, err := p.getUntil(raw, deadline)
	if err != nil || body == "" {
		return ""
	}

	// 直接在页面里找 m3u8/mp4
	for _, m := range extractURLsFromHTML(body, raw) {
		if r := p.resolveSingle(videoURL, m, deadline); r != "" {
			return r
		}
	}
	// iframe 嵌套深挖
	iframeRe := regexp.MustCompile(`(?i)<iframe[^>]+src=["']([^"']+)["']`)
	for _, m := range iframeRe.FindAllStringSubmatch(body, -1) {
		if len(m) < 2 {
			continue
		}
		src := resolveRelativeRef(raw, strings.TrimSpace(m[1]))
		if r := p.followNested(videoURL, src, deadline, deep-1); r != "" {
			return r
		}
	}
	return ""
}

// isReachableMedia 用小的 Range 请求确认地址确实返回媒体流，而非 404 / HTML。
func (p *VideoParser) isReachableMedia(u string, deadline time.Time) bool {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", directUA)
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("Referer", "https://www.iqiyi.com/")
	cl := p.client
	if timeout := time.Until(deadline); timeout > 0 {
		c2 := *p.client
		c2.Timeout = timeout
		cl = &c2
	} else {
		return false
	}

	resp, err := cl.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "text/html") || strings.Contains(ct, "application/json") || strings.Contains(ct, "text/plain") {
		// 明确是网页/接口文本，不是媒体流
		return false
	}
	switch {
	case resp.StatusCode == 200 || resp.StatusCode == 206:
		if strings.Contains(ct, "mpegurl") || strings.Contains(ct, "mp4") || strings.Contains(ct, "video/mp2t") ||
			strings.Contains(ct, "octet-stream") || strings.Contains(ct, "binary") {
			return true
		}
		return true // 多数 CDN 返回通用类型，能连通即视为可播
	default:
		return false
	}
}

// getUntil GET 请求直到 deadline，返回 body；失败返回空。
func (p *VideoParser) getUntil(raw string, deadline time.Time) (string, error) {
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", directUA)
	req.Header.Set("Referer", "https://www.iqiyi.com/")
	cl := p.client
	if timeout := time.Until(deadline); timeout > 0 {
		c2 := *cl
		c2.Timeout = timeout
		cl = &c2
	}
	resp, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil
	}
	// 用限长读取，m3u8 列表通常很小
	limited := &io.LimitedReader{R: resp.Body, N: 2 * 1024 * 1024}
	b, err := io.ReadAll(limited)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// extractURLsFromHTML 从 HTML 文本提取候选 m3u8/mp4 地址（含 JSON 转义符的处理）。
func extractURLsFromHTML(body, base string) []string {
	// 清理 JSON 转义：\/ -> /
	clean := strings.ReplaceAll(body, `\/`, `/`)
	// 提取协议相对(http:// 或 //)
	re := regexp.MustCompile(`(?i)((?:https?:)?//[^\s"'<>\\]+\.(?:m3u8|mp4)[^\s"'<>\\]*)`)
	seen := make(map[string]bool)
	var out []string
	for _, m := range re.FindAllString(clean, -1) {
		u := strings.TrimSpace(m)
		u = strings.Trim(u, `"'<>,;`)
		if u == "" {
			continue
		}
		u = resolveRelativeRef(base, u)
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// normalizeCandidateURL 补齐协议头并截断明显是 HTML/脏尾的字符串。
func normalizeCandidateURL(u string) string {
	u = strings.TrimSpace(u)
	if strings.HasPrefix(u, "//") {
		u = "https:" + u
	} else if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	// 若根正则判定不匹配媒体/页面则直接截断到首个明显分隔符
	u = strings.Trim(u, `"'<>`)
	return u
}

// resolveRelativeRef 将相对地址基于 base 解析为绝对地址。
func resolveRelativeRef(base, ref string) string {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	if strings.HasPrefix(ref, "//") {
		return "https:" + ref
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

// parseBandwidth 从 #EXT-X-STREAM-INF 行解析 BANDWIDTH 值。
func parseBandwidth(attrLine string) int64 {
	re := regexp.MustCompile(`BANDWIDTH=(\d+)`)
	m := re.FindStringSubmatch(attrLine)
	if len(m) < 2 {
		return -1
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return -1
	}
	return n
}
