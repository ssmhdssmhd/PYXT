// 模拟浏览器环境执行 LLQPlayer play.start.js 全流程,观察是否拿到 m3u8
// 使用相对路径读取本目录下的 engine.dat / code.min.js / play.start.js
const fs = require('fs');
const path = require('path');
const http = require('http');
const https = require('https');
const vm = require('vm');

const DIR = __dirname;

// ---- 浏览器环境 mock ----
const cookieStore = {};
global.window = global;
global.top = global;
global.navigator = { userAgent: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120.0.0.0 Safari/537.36', platform: 'Win32', language: 'zh-CN' };
global.location = { href: 'https://media.staticfile.link/?iv=3137322e37312e3130332e313538&key=0ff5fac30d8e8e9d2c738f74965c1e1e&url=https://www.iqiyi.com/v_1dp87w90j9w.html', hostname: 'media.staticfile.link', protocol: 'https:', search: '?iv=3137322e37312e3130332e313538&key=0ff5fac30d8e8e9d2c738f74965c1e1e&url=https://www.iqiyi.com/v_1dp87w90j9w.html' };
Object.defineProperty(global.document = {}, 'cookie', {
  get() { return Object.entries(cookieStore).map(([k, v]) => `${k}=${v}`).join('; '); },
  set(c) {
    const [kv, ...rest] = c.split(';');
    const [k, v] = kv.trim().split('=');
    const parts = rest.map(s => s.trim().toLowerCase());
    if (parts.includes('max-age=0') || parts.includes('expires=thu, 01 jan 1970 00:00:00 gmt')) delete cookieStore[k];
    else cookieStore[k] = v;
  }
});
document.documentElement = { style: {}, appendChild() {}, innerHTML: '' };
document.createElement = () => ({ style: {}, setAttribute() {}, appendChild() {}, removeChild() {}, addEventListener() {}, removeEventListener() {}, innerHTML: '', src: '', id: '', className: '', textContent: '', onclick: null });
document.getElementsByTagName = () => [];
document.querySelectorAll = () => [];
document.querySelector = () => null;
document.getElementById = () => null;
document.addEventListener = () => {};
document.body = { appendChild() {}, removeChild() {}, style: {}, innerHTML: '' };
if (!global.unescape) global.unescape = s => decodeURIComponent(s.replace(/%u([0-9a-fA-F]{4})/g, (m, h) => String.fromCharCode(parseInt(h, 16))));

// ---- hook eval:捕获解密后的 engine 文本 ----
const realEval = global.eval;
global.eval = function (code) {
  if (typeof code === 'string' && code.length > 1000) {
    console.log('[EVAL] len=' + code.length);
    const m3u8s = code.match(/[a-zA-Z0-9\/\.:_\-\?\&=%]{10,}\.m3u8/g);
    if (m3u8s) console.log('[EVAL] M3U8 FOUND:', JSON.stringify(m3u8s));
    const mp4s = code.match(/[a-zA-Z0-9\/\.:_\-\?\&=%]{10,}\.mp4/g);
    if (mp4s) console.log('[EVAL] MP4 FOUND:', JSON.stringify(mp4s));
    // 十六进制转义形式的 url
    const hexUrls = code.match(/(?:\\x[0-9a-fA-F]{2}){20,}/g);
    if (hexUrls) {
      const decoded = hexUrls.map(h => h.replace(/\\x([0-9a-fA-F]{2})/g, (m, b) => String.fromCharCode(parseInt(b, 16))));
      console.log('[EVAL] HEX-URLS:', JSON.stringify(decoded.slice(0, 10)));
    }
  }
  return realEval.call(global, code);
};

// ---- HTTP 请求(跟随重定向)----
function doRequest(method, url, data, extraHeaders) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const lib = u.protocol === 'https:' ? https : http;
    const payload = data ? (typeof data === 'string' ? data : JSON.stringify(data)) : null;
    const headers = Object.assign({ 'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120.0.0.0 Safari/537.36', Referer: 'https://media.staticfile.link/' }, extraHeaders || {});
    if (payload) headers['Content-Length'] = Buffer.byteLength(payload);
    const req = lib.request(u, { method, headers }, res => {
      if ([301, 302, 303, 307, 308].includes(res.statusCode) && res.headers.location) {
        res.resume();
        const next = new URL(res.headers.location, u).toString();
        return resolve(doRequest(method, next, data, extraHeaders));
      }
      let body = '';
      res.setEncoding('utf8');
      res.on('data', c => body += c);
      res.on('end', () => resolve({ status: res.statusCode, url, body }));
    });
    req.on('error', reject);
    if (payload) req.write(payload);
    req.end();
  });
}

// ---- jQuery mock:$.ajax 真正发请求,回调 ----
const $ = function () { return { on() {}, css() {}, append() {}, html() {}, attr() {}, remove() {}, find() { return []; } }; };
$.ajax = $.post = function (options, dataArg, successArg) {
  let opts = typeof options === 'string' ? { url: options, data: dataArg, success: successArg } : options;
  const target = opts.url || opts.ur;
  console.log('[REQ]', opts.method || 'GET', target);
  const respond = resp => {
    console.log('[RESP] status=' + resp.status + ' len=' + resp.body.length + ' head=' + resp.body.slice(0, 100).replace(/\n/g, ' '));
    if (opts.success) opts.success(resp.body, 'success', { status: resp.status });
    if (opts.complete) opts.complete({ status: resp.status });
  };
  if (target.includes('liqiang')) {
    // 用本目录下 engine.dat 模拟响应
    respond({ status: 200, url: target, body: fs.readFileSync(path.join(DIR, 'engine.dat'), 'utf8') });
  } else {
    doRequest(opts.method || 'GET', target, opts.data, opts.headers)
      .then(respond)
      .catch(e => { console.log('[REQ-ERR]', e.message); if (opts.error) opts.error(e); });
  }
  return { done() { return this; }, fail() { return this; } };
};
$.get = function (url, success) { return $.ajax({ url, method: 'GET', success }); };
$.getJSON = function (url, success) { return $.ajax({ url, method: 'GET', success }); };
global.$ = $;
global.jQuery = $;

// ---- CryptoJS ----
vm.runInThisContext(fs.readFileSync(path.join(DIR, 'code.min.js'), 'utf8'));

// ---- 播放器页面内联变量 ----
global.__ver__ = 'MS4yLjU=';
global.__cdn__ = 'c3RhdGljLWNkbi5ieXRlYW1vbmUuY24=';
global.appkey = '25a915635f131f3174bb0d4002e6d79b';
global.videoType = 'null';
global.parseLink = process.argv[2] || 'https://www.iqiyi.com/v_1dp87w90j9w.html';
global.KEYS = ['yV0vF1JBi0Snldwxst+CBseggXRKyDk4gGxov+aXhRMpLbwdfC4sR/AEfSEsrRqAW2N+iRNqD/ci2cWnz7aBi9yjoa1nPOOqpYEpaLR+t3FcF0DVVbM7UALtCEphcthGDSYZPPbFfYljJCrwQTeoTykr5ez/mre8lq7E3vZrwUGmFzi8yaUnHD2H0rt0EzP1HBFRx1lggP8SQdpsxqRd1fcCrDXFzH8AQLU=2oreep4q', '2qMJzCHXpJDxopvJKyem/XXUVOdg+AYAW2xLo5UDbM56BTvCstfUsMAA7MlZ6mPTNXA8WTbTG1VpwRNUSeFBhDC2UQhb6Sq3oB2V9mqadhkzY2oF4xv3qs8Aiy7GjSeCjkgsQfdbwdmiXliUCdl412saYHAYFoU5DRuxbH0kS5Kzdp8WpjnN5sQkNLzwPaERceuGlURUK+ZASDRdYzr0ecHqQWl9VHfU9y8=gXNP1ikP', 'aHvh6D55n0AI9WvcTpcK7adNEjyujlAkbiUnCXCbeeWdwNZeCcwoDm0CpWiklRoPKkueTurQDN9Pm4jV2vQqdL314Zw4AnqVeBT64IoTXjKbwmw8iJM/Bsp0UIGQoKNUCi5/7VYFvZKeMKIrQXennKYDmWFGlQzUy1VOLDsOp5YTbuaTBlepqE+n1f3oJ8/aSMJh8TWYfGH0vQ==ufH4qKhw'];

// ---- 执行 play.start.js ----
console.log('=== 执行 play.start.js ===');
try {
  vm.runInThisContext(fs.readFileSync(path.join(DIR, 'play.start.js'), 'utf8'));
  console.log('[play.start.js] 执行完成');
} catch (e) {
  console.error('[play.start.js] 执行出错:', e.message);
  console.error(e.stack.split('\n').slice(0, 8).join('\n'));
}
setTimeout(() => { console.log('[done] 等待完成'); process.exit(0); }, 25000);
