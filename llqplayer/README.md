# llqplayer - LLQPlayer 引擎资源目录

> 本目录存放 LLQPlayer 播放引擎相关资源，**更新时只需直接替换对应文件**，无需改动任何代码。

## 📂 目录说明

| 文件 | 作用 | 更新方式 |
|------|------|----------|
| `engine.dat` | LLQPlayer 引擎数据（核心） | 获取最新引擎后直接替换 |
| `play.start.js` | 播放器启动脚本 | 直接从播放页源码提取后替换 |
| `code.min.js` | CryptoJS 依赖库 | 一般无需更新 |
| `run_llq.js` | 本地解析测试脚本 | 一般无需更新 |

## 🔄 更新方法（用户可直接替换）

1. 打开需要解析的视频页面（如爱奇艺、腾讯视频等），按 `F12` 打开开发者工具
2. 找到播放器页面引用的 `play.start.js` 与 `engine` 相关请求，下载最新文件
3. 用下载的新文件**直接替换**本目录下的 `engine.dat` / `play.start.js`
4. 替换后执行以下命令验证是否生效：

```bash
node run_llq.js "https://www.iqiyi.com/v_xxx.html"
```

看到 `[EVAL] M3U8 FOUND` 或 `[EVAL] HEX-URLS` 输出即表示解析成功。

## ⚠️ 注意事项

- `engine.dat` 为引擎数据文件，**必须与 `play.start.js` 版本匹配**，否则可能解密失败
- 替换文件时请保持文件名不变（`engine.dat` / `play.start.js`）
- 若替换后解析失败，请恢复上一版文件，等待引擎更新后再试
