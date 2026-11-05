package tdx

import (
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// tdxNameSlotGBK 模拟 TDX 0x44d 接口的名称槽位：16 字节 GBK。
// 见 tdx/stock.go 中 `name := gbkClean(data[off+8 : off+24])`。
const tdxNameSlotGBK = 16

func gbkBytes(t *testing.T, s string) []byte {
	t.Helper()
	out, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatalf("GBK 编码 %q 失败: %v", s, err)
	}
	return out
}

// TestGbkCleanTruncatedNameSlot 覆盖「TDX 名称槽位只有 16 字节」导致的截断：
// 超长名称在数据源侧被按字节砍断，末尾可能只剩半个汉字，gbkClean 必须把它丢掉，
// 而不是交给解码器替换成 U+FFFD（那会在界面上显示成「半个字符乱码」）。
func TestGbkCleanTruncatedNameSlot(t *testing.T) {
	cases := []struct {
		full string // 完整名称（数据源已放不下）
		want string // 期望结果：可完整解码的前缀，且不含 U+FFFD
	}{
		{"上证中盘ETF易方达", "上证中盘ETF易方"},
		{"大宗商品ETF国联安", "大宗商品ETF国联"},
		{"10年地方债ETF海富通", "10年地方债ETF海"},
		{"恒生中国企业ETF易方达", "恒生中国企业ETF"},
		{"纳斯达克100ETF招商", "纳斯达克100ETF招"},
	}
	for _, c := range cases {
		raw := gbkBytes(t, c.full)
		if len(raw) <= tdxNameSlotGBK {
			t.Fatalf("%q 的 GBK 长度 %d 未超过槽位 %d，用例不成立", c.full, len(raw), tdxNameSlotGBK)
		}
		raw = raw[:tdxNameSlotGBK] // 数据源按字节截断

		got := gbkClean(raw)
		if got != c.want {
			t.Errorf("gbkClean(%q 截断) = %q, want %q", c.full, got, c.want)
		}
		if strings.ContainsRune(got, '\uFFFD') {
			t.Errorf("gbkClean(%q 截断) 结果仍含 U+FFFD: %q", c.full, got)
		}
	}
}

// TestGbkCleanKeepsCompleteNames 完整名称不应被改动。
func TestGbkCleanKeepsCompleteNames(t *testing.T) {
	cases := []struct {
		raw  []byte
		want string
	}{
		{gbkBytes(t, "沪深300ETF"), "沪深300ETF"},
		{gbkBytes(t, "上证中盘ETF易方达"), "上证中盘ETF易方达"},
		{[]byte("NASDAQ100ETF"), "NASDAQ100ETF"},
	}
	for _, c := range cases {
		if got := gbkClean(c.raw); got != c.want {
			t.Errorf("gbkClean(%v) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// TestGbkCleanEdgeCases 边界：NUL 填充、空输入、仅剩半个汉字。
func TestGbkCleanEdgeCases(t *testing.T) {
	// TDX 用 0 填充剩余字节
	padded := append(gbkBytes(t, "沪深300ETF"), 0, 0, 0)
	if got := gbkClean(padded); got != "沪深300ETF" {
		t.Errorf("NUL 填充: got %q, want %q", got, "沪深300ETF")
	}
	if got := gbkClean(nil); got != "" {
		t.Errorf("空输入: got %q, want 空串", got)
	}
	// 整段只剩一个 GBK 前导字节
	if got := gbkClean([]byte{0xB4}); got != "" {
		t.Errorf("仅半字节: got %q, want 空串", got)
	}
	if got := gbkClean(nil); strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("空输入结果含 U+FFFD: %q", got)
	}
}
