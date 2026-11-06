package model

import "testing"

func TestClassifyCodeSeparatesBShare(t *testing.T) {
	tests := []struct {
		symbol string
		want   string
	}{
		{symbol: "sh600000", want: ClassStock},
		{symbol: "sz000001", want: ClassStock},
		{symbol: "sh900936", want: ClassBStock},
		{symbol: "sz200541", want: ClassBStock},
		{symbol: "sh510300", want: ClassETF},
		{symbol: "sh113050", want: ClassBond},
		{symbol: "sz123001", want: ClassBond},
		{symbol: "sz128012", want: ClassBond},
	}

	for _, tt := range tests {
		t.Run(tt.symbol, func(t *testing.T) {
			if got := ClassifyCode(tt.symbol); got != tt.want {
				t.Fatalf("ClassifyCode(%q) = %q, want %q", tt.symbol, got, tt.want)
			}
		})
	}
}

func TestPriceScaleKeepsBSharePrecision(t *testing.T) {
	tests := []struct {
		symbol string
		want   float64
	}{
		{symbol: "sh600000", want: 100},
		{symbol: "sh900936", want: 1000},
		{symbol: "sz200541", want: 1000},
		{symbol: "sh510300", want: 1000},
		{symbol: "sh113050", want: 10000},
		{symbol: "sz123001", want: 10000},
	}

	for _, tt := range tests {
		t.Run(tt.symbol, func(t *testing.T) {
			if got := PriceScale(tt.symbol); got != tt.want {
				t.Fatalf("PriceScale(%q) = %v, want %v", tt.symbol, got, tt.want)
			}
		})
	}
}

func TestSymbolFromCodeKeepsBShareMapping(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{code: "600000", want: "sh600000"},
		{code: "900936", want: "sh900936"},
		{code: "200541", want: "sz200541"},
		{code: "510300", want: "sh510300"},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			got, ok := SymbolFromCode(tt.code)
			if !ok {
				t.Fatalf("SymbolFromCode(%q) did not resolve", tt.code)
			}
			if got != tt.want {
				t.Fatalf("SymbolFromCode(%q) = %q, want %q", tt.code, got, tt.want)
			}
		})
	}
}

func TestIsCoreSecurity(t *testing.T) {
	tests := []struct {
		symbol string
		want   bool
	}{
		// 核心品种: 股票/ETF/指数/板块指数/债券转债
		{"sh600000", true},
		{"sz000001", true},
		{"sz300750", true},
		{"sh688981", true},
		{"bj920002", true},
		{"sh510300", true},
		{"sz159915", true},
		{"sh000300", true},
		{"sz399001", true},
		{"sh880001", true},
		{"sh113050", true},
		{"sz123001", true},
		{"bj821001", true},
		// 非核心/期权/逆回购/B股等
		{"sh900936", false}, // B股
		{"sz200541", false}, // B股
		{"sh232113", false}, // 沪市期权
		{"sz104303", false}, // 深市期权
		{"sz563772", false}, // sz5开头期权
		{"sh171740", false}, // 逆回购/未知
		{"sz131810", false}, // 深市逆回购 R-001
		{"sh204001", false}, // 沪市逆回购 GC001
		{"sh019536", false}, // 记账式国债
		{"sh186905", false}, // 逆回购/未知
		{"unknown", false},
	}

	for _, tt := range tests {
		t.Run(tt.symbol, func(t *testing.T) {
			if got := IsCoreSecurity(tt.symbol); got != tt.want {
				t.Fatalf("IsCoreSecurity(%q) = %v, want %v", tt.symbol, got, tt.want)
			}
		})
	}
}
