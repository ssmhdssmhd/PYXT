package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

//go:embed api_docs.html player.html video_player.html
var staticFiles embed.FS

const version = "v0.0.7"

// parseRequest 解析请求参数
type parseRequest struct {
	URL     string
	ShowURL bool
}

// 全局解析器实例
var parser = NewVideoParser()

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/parse", handleParse)
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
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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
