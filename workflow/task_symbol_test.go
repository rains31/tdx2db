package workflow

import (
	"testing"

	"github.com/jing2uo/tdx2db/model"
)

// TestPreferredSymbolName 覆盖「重导时名称保优」的各种情形。
// 背景：TDX 行情协议名称槽位固定 16 字节 GBK，超长名称在数据源侧就被截断；
// 库里可能存有更完整的名称（交易所/数据商扩位简称），重导不应把它打回截断版本。
func TestPreferredSymbolName(t *testing.T) {
	cases := []struct {
		name string
		old  string
		new  string
		want string
	}{
		// 旧名更完整：新名是旧名的前缀（被 16 字节槽位截断）-> 保旧名
		{"截断保优", "上证中盘ETF易方达", "上证中盘ETF易方", "上证中盘ETF易方达"},
		{"静默截断保优", "沪深300ETF华泰柏瑞", "沪深300ETF华泰柏", "沪深300ETF华泰柏瑞"},
		{"多字保优", "易方达中证港股通信息技术综合ETF", "港股通信息技术ET", "易方达中证港股通信息技术综合ETF"},

		// 旧名带乱码、新名干净 -> 取新名（让 gbkClean 的修复生效）
		{"旧乱码取新", "上证中盘ETF易方\ufffd", "上证中盘ETF易方", "上证中盘ETF易方"},

		// 新名带乱码、旧名干净 -> 保旧名（防止旧二进制/异常数据把乱码写回来）
		{"新乱码保旧", "上证中盘ETF易方达", "上证中盘ETF易方\ufffd", "上证中盘ETF易方达"},

		// 真实改名 / 口径变更：新名不是旧名前缀 -> 取新名
		{"改名取新", "油气ETF华宝", "标普油气ETF华宝", "标普油气ETF华宝"},
		{"完全不同取新", "恒生科技ETF天弘", "天弘恒生科技ETF", "天弘恒生科技ETF"},

		// 旧名不存在 / 为空 -> 取新名
		{"无旧名", "", "创业板ETF易方达", "创业板ETF易方达"},
		// 新名为空 -> 保旧名
		{"新名为空", "创业板ETF易方达", "", "创业板ETF易方达"},
		// 完全相同
		{"相同", "创业板ETF易方达", "创业板ETF易方达", "创业板ETF易方达"},
		// 新名更长（更完整）-> 取新名
		{"新名更长", "中盘ETF", "上证中盘ETF易方", "上证中盘ETF易方"},
		// 前后空白应被清理
		{"空白清理", "  上证中盘ETF易方达 ", " 上证中盘ETF易方 ", "上证中盘ETF易方达"},
	}

	for _, c := range cases {
		if got := preferredSymbolName(c.old, c.new); got != c.want {
			t.Errorf("%s: preferredSymbolName(%q, %q) = %q, want %q", c.name, c.old, c.new, got, c.want)
		}
	}
}

// TestMergeSymbolNames 校验合并时「行集与 class 取新值、名称择优」。
func TestMergeSymbolNames(t *testing.T) {
	existing := []model.SymbolName{
		{Symbol: "sh510130", Name: "上证中盘ETF易方达", Class: "etf"},
		{Symbol: "sh510300", Name: "沪深300ETF华泰柏瑞", Class: "etf"},
		{Symbol: "sh999999", Name: "已退市标的", Class: "etf"}, // 新数据中不存在 -> 应被丢弃
	}
	incoming := []model.SymbolName{
		{Symbol: "sh510130", Name: "上证中盘ETF易方", Class: "etf"},
		{Symbol: "sh510300", Name: "沪深300ETF华泰柏", Class: "etf"},
		{Symbol: "sz159915", Name: "创业板ETF易方达", Class: "etf"},
		{Symbol: "sh600000", Name: "浦发银行", Class: "stock"},
	}

	merged, preserved := mergeSymbolNames(existing, incoming)

	if preserved != 2 {
		t.Errorf("preserved = %d, want 2", preserved)
	}
	if len(merged) != len(incoming) {
		t.Errorf("行数 = %d, want %d（行集应以新数据为准）", len(merged), len(incoming))
	}

	want := map[string]string{
		"sh510130": "上证中盘ETF易方达",
		"sh510300": "沪深300ETF华泰柏瑞",
		"sz159915": "创业板ETF易方达",
		"sh600000": "浦发银行",
	}
	for _, item := range merged {
		if want[item.Symbol] != item.Name {
			t.Errorf("%s 名称 = %q, want %q", item.Symbol, item.Name, want[item.Symbol])
		}
	}

	for _, item := range merged {
		if item.Symbol == "sh999999" {
			t.Error("已退市标的（新数据中不存在）不应被保留")
		}
	}

	// class 取新值
	for _, item := range merged {
		if item.Symbol == "sh600000" && item.Class != "stock" {
			t.Errorf("class 应取新值，got %q", item.Class)
		}
	}
}
