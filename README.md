# PYXT - 视频解析 API 服务 (Go 版)

> 由 Python 版 [视频解析 + API](视频解析工具) 项目移植重构的 Go 版本
>
> **版本:** v0.0.5 | **更新日期:** 2026-09-20 | **状态:** ✅ 可运行

一个高性能的视频解析 HTTP API 服务，使用 Go 编写。提供视频链接解析接口，支持爱奇艺、腾讯视频、优酷、芒果TV 等主流平台，返回可直接播放的 m3u8 / mp4 视频源地址。

## ✨ 功能特点

- 🚀 **Go 原生高性能**：单二进制部署，无运行时依赖，内存占用极低
- 📡 **标准 JSON API**：GET/POST 双方式调用，方便任何语言对接
- 🔗 **多平台支持**：爱奇艺、腾讯视频、优酷、芒果TV 等
- 🎯 **多级解析策略**：第三方接口 → 页面直解析 → 通用播放器链接
- 🌐 **跨域支持**：内置 CORS 中间件，前端可直接调用
- 📖 **内置 API 文档**：在线测试工具，开箱即用
- 📦 **静态资源内嵌**：`go:embed` 打包，部署只需一个文件

## 📦 快速开始

### 本地运行

```bash
# 直接运行
go run .

# 或编译后运行
go build -o pyxt .
./pyxt
```

默认监听 `0.0.0.0:5000`，可通过环境变量 `PORT` 修改端口。

### Docker 运行

```bash
docker build -t pyxt .
docker run -d -p 5000:5000 pyxt
```

### 下载预编译版本

前往 [Releases](https://github.com/ssmhdssmhd/PYXT/releases) 下载对应平台的二进制文件（由 GitHub Actions 自动构建）。

## 📡 API 接口

### 1. 视频解析

```
GET /api/parse?url=<视频链接>&show_url=<1|0>
POST /api/parse
```

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| url | string | ✅ | 视频页面链接 |
| show_url | string | ❌ | 失败时是否返回URL，1=是，0=否，默认=1 |

**POST 请求体 (JSON):**

```json
{
  "url": "https://www.iqiyi.com/v_xxx.html",
  "show_url": "1"
}
```

**成功响应 (200):**

```json
{
  "code": 200,
  "msg": "获取成功",
  "title": "电影名称",
  "type": "m3u8",
  "url": "https://xxx.com/video.m3u8",
  "from": "https://www.iqiyi.com/v_xxx.html",
  "time": 0.123456
}
```

**响应字段说明：**

| 字段 | 类型 | 说明 |
|------|------|------|
| code | integer | 200=成功，400=参数错误，404=未找到，500=错误 |
| msg | string | 返回消息 |
| title | string | 视频标题 |
| type | string | 视频类型：m3u8、mp4、flv |
| url | string | 视频播放地址 |
| from | string | 原始视频链接 |
| time | float | 解析耗时（秒） |

### 2. 健康检查

```
GET /api/health
```

```json
{
  "status": "ok",
  "service": "视频解析 API",
  "version": "v0.0.3"
}
```

### 3. API 文档页面

访问 `http://localhost:5000/` 即可查看完整的交互式 API 文档，支持在线测试。

## 💻 调用示例

```bash
# cURL - GET
curl "http://localhost:5000/api/parse?url=https://www.iqiyi.com/v_xxx.html&show_url=1"

# cURL - POST
curl -X POST http://localhost:5000/api/parse \
  -H "Content-Type: application/json" \
  -d '{"url":"https://www.iqiyi.com/v_xxx.html","show_url":"1"}'
```

```python
# Python
import requests
response = requests.get("http://localhost:5000/api/parse",
                        params={"url": "https://www.iqiyi.com/v_xxx.html", "show_url": "1"})
print(response.json())
```

```javascript
// JavaScript
fetch('http://localhost:5000/api/parse?url=https://www.iqiyi.com/v_xxx.html&show_url=1')
    .then(r => r.json())
    .then(data => console.log(data));
```

## 🗂️ 项目结构

```
PYXT/
├── main.go              # HTTP 服务、路由、CORS、静态资源嵌入
├── parser.go            # 视频解析核心逻辑
├── llqplayer/           # LLQPlayer 引擎资源目录（可直接替换更新）
│   ├── engine.dat       #   引擎数据（核心，替换后无需改代码）
│   ├── play.start.js    #   播放器启动脚本
│   ├── code.min.js      #   CryptoJS 依赖库
│   ├── run_llq.js       #   本地解析测试脚本
│   └── README.md        #   更新说明
├── api_docs.html        # API 文档页面（内置在线测试）
├── player.html          # 通用播放器页面
├── video_player.html    # 视频播放页面
├── go.mod               # Go 模块定义
├── .github/
│   └── workflows/
│       └── build.yml    # GitHub Actions 云端构建配置
└── README.md            # 本文件
```

## 🔧 技术说明

- 解析逻辑与原 Python 版 `video_parser_api.py` 保持**完全一致**，包括多级解析策略、标题提取选择器、URL 校验规则
- HTML 解析使用 [goquery](https://github.com/PuerkitoBio/goquery)（BeautifulSoup 的 Go 等价物）
- 静态页面通过 `go:embed` 内嵌进二进制，部署零依赖
- 支持 Windows / Linux / macOS 三平台交叉编译

## 📝 更新日志

### v0.0.5 - 2026-09-20

- 🚀 端口占用自动切换：5000 被占用时自动尝试 5001~5009，并打印实际使用的端口，重复启动不再报 `address already in use`
- ✅ 编译命令统一使用 `-o` 命名：`go build -o pyxt .`（可按需改名）
- ✅ 实测：5000 被占用时自动切换 5001，健康检查与解析接口均正常

### v0.0.4 - 2026-09-20

- 📂 新增 `llqplayer/` 引擎资源目录：集中存放 LLQPlayer 引擎数据与播放器脚本，后续更新只需直接替换 `engine.dat` / `play.start.js`，无需改动代码
- 🔧 附带 `run_llq.js` 本地解析测试脚本（相对路径引用，可直接运行验证引擎是否生效）
- ✅ 更新说明见 `llqplayer/README.md`

### v0.0.3 - 2026-09-20

- ✨ 修复标题提取失败问题：PC 端页面反爬（爱奇艺等返回通用首页）时，自动回退到移动端域名 + 移动端 UA 提取标题与描述
- ✅ 实测爱奇艺链接正确返回标题（如"我会好好的""生万物第6集"），支持爱奇艺/腾讯/优酷/B 站移动端回退
- 📌 注：无法提取标题时仍返回"未知视频"兜底，不影响播放源解析

### v0.0.2 - 2026-09-20

- 🐛 修复 gzip 响应乱码问题：移除手动设置的 `Accept-Encoding` 头，由 Go 自动解压，恢复第三方接口播放源提取与页面解析能力
- ✅ 实测爱奇艺、B 站链接均能提取真实播放器地址（耗时约 0.2s）

### v0.0.1 - 2026-09-20

- 🎉 首个 Go 版本发布，由 Python 版完整移植
- ✅ 实现 `/api/parse`、`/api/health`、`/` 全部接口
- ✅ 移植全部解析逻辑：多平台解析、多级解析策略、标题提取
- ✅ 内置 API 文档与在线测试页面
- ✅ 配置 GitHub Actions 三平台自动构建
- ✅ 本地编译测试通过，接口验证通过

## 📄 许可证

本项目仅供学习研究使用，请遵守相关法律法规，勿用于商业用途。

## 📧 联系

- GitHub: [ssmhdssmhd](https://github.com/ssmhdssmhd)
