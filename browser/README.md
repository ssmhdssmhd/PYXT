# browser - 浏览器资源目录（无头浏览器兜底用）

> 本目录存放无头 Chromium 浏览器资源包，**由用户自行下载部署，与程序完全解耦**。
> 更新浏览器时只需**直接替换本目录下的资源**，无需改动任何代码；程序更新同理，互不影响。

## 📂 目录说明

| 文件/目录 | 作用 | 更新方式 |
|-----------|------|----------|
| `chrome-linux64/` | Linux 版 Chromium（含 `chrome` 可执行文件） | 下载新版直接替换 |
| `chrome-win64/` | Windows 版 Chromium（含 `chrome.exe`） | 下载新版直接替换 |
| `chrome-mac/` | macOS 版 Chromium | 下载新版直接替换 |

> 目录名可自定义，程序会递归扫描 `browser/` 下常见可执行文件名
> （`chrome` / `chromium` / `headless_shell` / `chrome-headless-shell` / `chrome.exe` 等），
> 命中即使用。不存在的目录/文件直接忽略，不影响程序运行。

## 🔄 部署方法（用户自行操作）

1. 下载 Chromium 资源包（任选其一）：
   - **Chrome for Testing**（官方）：https://googlechromelabs.github.io/chrome-for-testing/
   - **chrome-headless-shell**（更轻量，专为无头场景）：同上页面选择 `chrome-headless-shell`
   - 国内镜像：`https://npmmirror.com/mirrors/chrome-for-testing/`
2. 解压后把整个目录放入本 `browser/` 目录，例如：

```text
browser/
└── chrome-linux64/
    ├── chrome          ← 可执行文件（Linux）
    ├── chrome-wrapper
    └── resources/...
```

3. 启动 PYXT 服务，日志出现 `无头浏览器已启动并常驻复用` 即部署成功。

## ⚙️ 其他指定方式

- **环境变量 `CHROME_BIN`**：直接指定浏览器可执行文件的绝对路径，优先级最高
  ```bash
  export CHROME_BIN=/path/to/chrome
  ```
- 若系统 PATH 中已安装 Chrome/Chromium，程序也会自动识别，无需部署本目录

## ⚠️ 注意事项

- **程序不再自动下载浏览器**：未部署浏览器时，加密源兜底会自动跳过，普通解析不受影响
- **浏览器与程序分开更新**：升级浏览器只替换本目录；升级程序只替换程序文件，两者互不依赖
- **常驻复用**：浏览器进程首次使用时启动，之后解析请求复用同一实例，仅新建/关闭页面，速度更快
- 浏览器异常退出时程序会自动重建实例，无需重启服务
- 可用环境变量 `PYXT_HEADLESS=0` 完全关闭无头浏览器兜底
