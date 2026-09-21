package main

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

//go:embed api_docs.html player.html video_player.html
var staticFiles embed.FS

const version = "v0.0.9"

// parseRequest 解析请求参数
type parseRequest struct {
	URL     string
	ShowURL bool
}

// 全局解析器实例
var parser = NewVideoParser()

func main() {
	defer closeHeadlessBrowser() // 程序退出时关闭常驻无头浏览器

	mux := http.NewServeMux()

	mux.HandleFunc("/api/parse", handleParse)
	mux.HandleFunc("/api/proxy", handleProxy)
	mux.HandleFunc("/api/health", handleHealth)
	mux.HandleFunc("/", handleIndex)

	// 嵌入的静态文件（player 页面等）
	sub, err := fs.Sub(staticFiles, ".")
	if err == nil {
		mux.Handle("/player.html", http.FileServer(http.FS(sub)))
		mux.Handle("/video_player.html", http.FileServer(http.FS(sub)))
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "5000"
	}

	log.Println("============================================================")
	log.Println("🎬 视频解析 API 服务 (Go 版) " + version + " 启动")
	log.Println("============================================================")
	startServer(mux, port)
}

// startServer 启动 HTTP 服务并阻塞运行；指定端口被占用时自动尝试下一个端口(最多 +10)
func startServer(mux *http.ServeMux, basePort string) {
	portNum, err := strconv.Atoi(basePort)
	if err != nil {
		log.Fatalf("无效的 PORT 环境变量: %s", basePort)
	}
	for i := 0; i < 10; i++ {
		p := portNum + i
		addr := "0.0.0.0:" + strconv.Itoa(p)
		srv := &http.Server{
			Addr:         addr,
			Handler:      corsMiddleware(mux),
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 60 * time.Second,
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			if strings.Contains(err.Error(), "address already in use") {
				log.Printf("⚠️  端口 %d 被占用，自动尝试端口 %d ...", p, p+1)
				continue
			}
			log.Fatalf("服务启动失败: %v", err)
		}
		log.Println("============================================================")
		log.Printf("📡 API 地址: http://localhost:%d/api/parse", p)
		log.Printf("📖 文档地址: http://localhost:%d/", p)
		log.Printf("💚 健康检查: http://localhost:%d/api/health", p)
		log.Println("============================================================")
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("服务运行异常: %v", err)
		}
		return
	}
	log.Fatalf("端口 %d~%d 均被占用，请先停止旧进程后重试", portNum, portNum+9)
}

// corsMiddleware 允许跨域请求（与原 Flask CORS 一致）
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Range")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleParse 视频解析 API 接口
// 请求参数: url(必填), show_url(可选, 1=失败时返回URL, 0=不返回, 默认1)
func handleParse(w http.ResponseWriter, r *http.Request) {
	req, err := parseParams(r)
	if err != nil {
		writeJSON(w, http.StatusOK, ParseResult{
			Code:  400,
			Msg:   err.Error(),
			Title: "",
			Type:  "",
			URL:   "",
			From:  "",
			Time:  0,
		})
		return
	}

	result := parser.ParseVideo(req.URL, req.ShowURL)
	writeJSON(w, http.StatusOK, result)
}

// parseParams 解析 GET/POST 参数
func parseParams(r *http.Request) (*parseRequest, error) {
	req := &parseRequest{ShowURL: true}

	switch r.Method {
	case http.MethodGet:
		req.URL = strings.TrimSpace(r.URL.Query().Get("url"))
		req.ShowURL = r.URL.Query().Get("show_url") != "0"
	case http.MethodPost:
		var body struct {
			URL     string `json:"url"`
			ShowURL string `json:"show_url"`
		}
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&body); err != nil {
			// 兼容非 JSON 的 POST
			req.URL = strings.TrimSpace(r.FormValue("url"))
			req.ShowURL = r.FormValue("show_url") != "0"
		} else {
			req.URL = strings.TrimSpace(body.URL)
			req.ShowURL = body.ShowURL != "0"
		}
	default:
		return nil, &paramError{"仅支持 GET/POST 请求"}
	}

	if req.URL == "" {
		return nil, &paramError{"缺少必填参数: url"}
	}
	return req, nil
}

type paramError struct{ msg string }

func (e *paramError) Error() string { return e.msg }

// handleHealth 健康检查接口
func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"service": "视频解析 API",
		"version": version,
	})
}

// handleProxy 媒体代理接口：跨域转发 m3u8/mp4 等媒体流
// 请求参数: url(必填, 目标媒体地址)
// 作用:
//  1. 为所有响应附加 CORS 头, 解决第三方 CDN 无跨域头导致的播放失败
//  2. 支持 Range 请求(视频拖动进度条)
//  3. m3u8 内容中的子流/分片地址重写为代理地址, 保证整条链路同源
func handleProxy(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSpace(r.URL.Query().Get("url"))
	if target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少必填参数: url"})
		return
	}
	// 统一修正畸形地址(如上游输出的 https:///host/path 三斜杠), 后续校验与转发均基于修正后的地址
	target = normalizeMediaURL(target)
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url 参数非法, 仅支持 http/https 地址"})
		return
	}

	// 转发请求(透传 Range, 使用浏览器 UA)
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36")
	req.Header.Set("Referer", u.Scheme+"://"+u.Host+"/")
	if rng := r.Header.Get("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}
	// 透传 cookie(部分 CDN 依赖 cookie 鉴权)
	if ck := r.Header.Get("Cookie"); ck != "" {
		req.Header.Set("Cookie", ck)
	}

	resp, err := proxyClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "上游请求失败: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	// 统一附加 CORS 头
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges, Content-Type")

	ct := resp.Header.Get("Content-Type")
	isM3U8 := strings.Contains(strings.ToLower(ct), "mpegurl") || strings.HasSuffix(strings.ToLower(u.Path), ".m3u8")

	if isM3U8 {
		// m3u8 播放列表: 重写子流/分片地址为代理地址(避免浏览器跨域请求子流)
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if readErr != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "读取 m3u8 失败: " + readErr.Error()})
			return
		}
		rewritten := rewriteM3U8(string(body), target)
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(rewritten))
		return
	}

	// 普通媒体流: 透传状态与关键头, 流式转发(支持 Range 断点续传)
	w.Header().Set("Content-Type", ct)
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	w.Header().Set("Accept-Ranges", "bytes")
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		w.Header().Set("Content-Range", cr)
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// normalizeMediaURL 修正上游 CDN 的不规范地址: https:///host/path -> https://host/path
func normalizeMediaURL(raw string) string {
	if strings.Contains(raw, ":///") {
		raw = strings.Replace(raw, ":///", "://", 1)
	}
	return raw
}

// proxyClient 媒体代理专用客户端(与解析器复用相同传输层配置)
var proxyClient = &http.Client{
	Timeout: 120 * time.Second,
}

// rewriteM3U8 将 m3u8 内容中的子流/分片地址全部重写为 /api/proxy 代理地址
// baseURL 为 m3u8 自身地址, 用于解析相对路径
func rewriteM3U8(content, baseURL string) string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return content
	}
	rewriteURI := func(raw string) string {
		ref, refErr := url.Parse(strings.TrimSpace(raw))
		if refErr != nil {
			return raw
		}
		abs := base.ResolveReference(ref).String()
		// 修正上游 CDN 的不规范输出: https:///host/path -> https://host/path
		abs = normalizeMediaURL(abs)
		return "/api/proxy?url=" + url.QueryEscape(abs)
	}

	var sb strings.Builder
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "" || strings.HasPrefix(trimmed, "#EXT-X-KEY") || strings.HasPrefix(trimmed, "#EXT-X-MAP"):
			// 处理行内 URI="..." 属性(如 #EXT-X-KEY:METHOD=AES-128,URI="key.bin")
			out := line
			for {
				idx := strings.Index(out, `URI="`)
				if idx < 0 {
					break
				}
				start := idx + len(`URI="`)
				end := strings.Index(out[start:], `"`)
				if end < 0 {
					break
				}
				oldVal := out[start : start+end]
				out = out[:start] + rewriteURI(oldVal) + out[start+end:]
			}
			sb.WriteString(out)
		case strings.HasPrefix(trimmed, "#"):
			// 其他标签行原样保留
			sb.WriteString(line)
		default:
			// 非注释行即子流/分片地址, 全部转代理
			sb.WriteString(rewriteURI(trimmed))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// handleIndex API 文档页面
func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := staticFiles.ReadFile("api_docs.html")
	if err != nil {
		http.Error(w, "文档页面缺失", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// writeJSON 输出 JSON 响应
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("JSON 编码失败: %v", err)
	}
}
