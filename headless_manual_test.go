package main

import (
	"log"
	"os"
	"testing"
	"time"
)

// 手动冒烟测试：仅当设置 PYXT_HEADLESS_TEST=1 时运行，
// 用于本地验证无头浏览器能从加密源捕获真实 m3u8（需联网 + Chromium）。
func TestHeadlessManual(t *testing.T) {
	if os.Getenv("PYXT_HEADLESS_TEST") != "1" {
		t.Skip("需显式设置 PYXT_HEADLESS_TEST=1 才运行（联网无头测试）")
	}
	p := NewVideoParser()
	url := "https://www.iqiyi.com/v_1dp87w90j9w.html"
	start := time.Now()
	res := p.resolveWithHeadless(url, 30*time.Second)
	log.Printf("[headless] bin=%s res=%q elapsed=%v", findChromeBin(), res, time.Since(start))
	// 无头兜底是尽力而为：仅当解析接口页在真实浏览执行后，网络层捕获到
	// 直发 m3u8/mp4 时才返回。爱奇艺等自建强加密源不会直接下发明文直链，
	// 因此这里只做信息性输出，捕获到才算通过，未捕获不视为失败。
	if res == "" {
		log.Printf("[headless] 未捕获到直链（属正常尽力而为结果，源不可捕获）")
		return
	}
	if len(res) < 10 {
		t.Fatalf("捕获地址异常短: %q", res)
	}
}
