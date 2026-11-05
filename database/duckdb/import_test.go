package duckdb

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestDuckDBImportMissing(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	u, err := url.Parse("duckdb://" + dbPath)
	if err != nil {
		t.Fatal(err)
	}

	driver, err := NewDuckDBDriver(u)
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Connect(); err != nil {
		t.Fatal(err)
	}
	defer driver.Close()

	if err := driver.InitSchema(); err != nil {
		t.Fatal(err)
	}

	// 1. 构造日常数据并测试缺失插入
	csv1 := filepath.Join(tmpDir, "daily1.csv")
	content1 := `symbol,open,high,low,close,amount,volume,date
sh600000,10.0,10.5,9.9,10.2,100000,5000,2026-11-05
sh600001,15.0,15.5,14.9,15.2,200000,8000,2026-11-05
`
	if err := os.WriteFile(csv1, []byte(content1), 0644); err != nil {
		t.Fatal(err)
	}

	n1, err := driver.ImportKlineDailyMissing(csv1)
	if err != nil {
		t.Fatalf("first import failed: %v", err)
	}
	if n1 != 2 {
		t.Fatalf("expected 2 inserted, got %d", n1)
	}

	// 2. 构造包含重复和新记录的 CSV
	csv2 := filepath.Join(tmpDir, "daily2.csv")
	content2 := `symbol,open,high,low,close,amount,volume,date
sh600000,10.0,10.5,9.9,10.2,100000,5000,2026-11-05
sh600001,15.0,15.5,14.9,15.2,200000,8000,2026-11-05
sh600002,20.0,20.5,19.9,20.2,300000,9000,2026-11-05
`
	if err := os.WriteFile(csv2, []byte(content2), 0644); err != nil {
		t.Fatal(err)
	}

	n2, err := driver.ImportKlineDailyMissing(csv2)
	if err != nil {
		t.Fatalf("second import failed: %v", err)
	}
	if n2 != 1 {
		t.Fatalf("expected 1 new row inserted (sh600002), got %d", n2)
	}

	// 3. 再次导入相同数据，应该插入 0 条
	n3, err := driver.ImportKlineDailyMissing(csv2)
	if err != nil {
		t.Fatalf("third import failed: %v", err)
	}
	if n3 != 0 {
		t.Fatalf("expected 0 new rows inserted, got %d", n3)
	}

	// 4. 测试分钟线缺失导入
	minCsv1 := filepath.Join(tmpDir, "1min1.csv")
	minContent1 := `symbol,open,high,low,close,amount,volume,datetime
sh600000,10.0,10.1,9.9,10.0,1000,100,"2026-11-05T09:31:00+08:00"
sh600000,10.1,10.2,10.0,10.2,1200,120,"2026-11-05T09:32:00+08:00"
`
	if err := os.WriteFile(minCsv1, []byte(minContent1), 0644); err != nil {
		t.Fatal(err)
	}

	mn1, err := driver.ImportKline1MinMissing(minCsv1)
	if err != nil {
		t.Fatalf("first 1min import failed: %v", err)
	}
	if mn1 != 2 {
		t.Fatalf("expected 2 1min inserted, got %d", mn1)
	}

	// 再次导入完全相同的数据，应该插入 0
	mn2, err := driver.ImportKline1MinMissing(minCsv1)
	if err != nil {
		t.Fatalf("second 1min import failed: %v", err)
	}
	if mn2 != 0 {
		t.Fatalf("expected 0 new 1min rows inserted, got %d", mn2)
	}
}
