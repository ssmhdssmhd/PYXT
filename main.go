package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
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

//go:embed admin/login.html admin/index.html
var adminFiles embed.FS

const version = "v0.0.11"

// 后台管理：凭据与会话
const (
	adminCookieName = "pyxt_admin"
	adminSessionKey = "PYXT_ADMIN_SESSION_KEY" // 未另设则使用随机密钥
	adminTokenTTL   = 12 * time.Hour
	adminSessionTTL = 12 * time.Hour
)

// adminCredentials 后台登录凭据（可环境变量覆盖，默认 admin/123456）
func adminCredentials() (user, pass string) {
	user = os.Getenv("ADMIN_USER")
	if user == "" {
		user = "admin"
	}
	pass = os.Getenv("ADMIN_PASS")
	if pass == "" {
		pass = "123456"
	}
	return user, pass
}

// adminSessionSecret 会话签名密钥（启动时生成一次并常驻内存）
var adminSessionSecret = func() []byte {
	if k := os.Getenv(adminSessionKey); k != "" {
		return []byte(k)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("生成会话密钥失败: %v", err)
	}
	return b
}()

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
	mux.HandleFunc("/mx/", handleAdmin)
	mux.HandleFunc("/mx", handleAdmin)
	mux.HandleFunc("/api/admin/login", handleAdminLogin)
	mux.HandleFunc("/api/admin/logout", handleAdminLogout)
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
	log.Printf("🔌 解析源 %d 个（来源: jiexiyuan.txt / PYXT_PARSE_APIS / 内置默认）", len(parser.parseAPIs))
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

// ==================== 后台管理（/mx/） ====================

// handleAdmin 后台路由：/mx/（主页）或 /mx/login（登录页）
func handleAdmin(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	isLogin := path == "/mx/login" || path == "/mx/login/"
	if isLogin {
		// 登录页：已登录则直接进后台，否则显示登录页
		if adminAuthed(r) {
			http.Redirect(w, r, "/mx/", http.StatusFound)
		} else {
			serveAdminFile(w, "admin/login.html")
		}
		return
	}
	// 后台主页：未登录跳转登录页
	if !adminAuthed(r) {
		http.Redirect(w, r, "/mx/login", http.StatusFound)
		return
	}
	serveAdminFile(w, "admin/index.html")
}

// serveAdminFile 输出后台静态页面（调用处已校验会话）
func serveAdminFile(w http.ResponseWriter, file string) {
	if file == "admin/index.html" {
		w.Header().Set("Cache-Control", "no-store")
	}
	data, err := adminFiles.ReadFile(file)
	if err != nil {
		http.Error(w, "页面缺失", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// adminAuthed 校验后台 Cookie 令牌是否有效且未过期
func adminAuthed(r *http.Request) bool {
	tok, err := r.Cookie(adminCookieName)
	if err != nil || tok.Value == "" {
		return false
	}
	parts := strings.Split(tok.Value, ".")
	if len(parts) != 2 {
		return false
	}
	// 校验签名
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(sig, adminSign(parts[0])) {
		return false
	}
	// 解析载荷并检查过期
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	var p struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.Exp < time.Now().Unix() {
		return false
	}
	return true
}

// handleAdminLogin 后台登录接口
func handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"code": "405", "msg": "仅支持 POST"})
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 400, "msg": "请求格式错误"})
		return
	}
	user, pass := adminCredentials()
	if req.Username != user || req.Password != pass {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 401, "msg": "用户名或密码错误"})
		return
	}

	// 签发令牌: base64url(payload).base64url(signature)
	exp := time.Now().Add(adminSessionTTL).Unix()
	payloadJSON, _ := json.Marshal(map[string]interface{}{"u": user, "exp": exp})
	payload := base64.RawURLEncoding.EncodeToString(payloadJSON)
	token := payload + "." + base64.RawURLEncoding.EncodeToString(adminSign(payload))
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(adminSessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "登录成功"})
}

// handleAdminLogout 退出登录
func handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"code": "405", "msg": "仅支持 POST"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已退出"})
}

// adminSign 计算载荷的 HMAC-SHA256 签名
func adminSign(payload string) []byte {
	mac := hmac.New(sha256.New, adminSessionSecret)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

// writeJSON 输出 JSON 响应
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("JSON 编码失败: %v", err)
	}
}
