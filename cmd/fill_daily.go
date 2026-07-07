package cmd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jing2uo/tdx2db/calc"
	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/tdx"
	"github.com/jing2uo/tdx2db/utils"
)

const fillTable = "raw_kline_daily_fill"

// FillDaily 用完整 vipdoc 目录一次性补全 raw_kline_daily 中缺失的日线数据
func FillDaily(ctx context.Context, dbURI string, vipdocDir string) error {
	db, err := database.NewDB(dbURI)
	if err != nil {
		return fmt.Errorf("create db driver: %w", err)
	}
	if err := db.Connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	if err := db.InitSchema(); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	defer db.Close()

	if err := utils.CheckDirectory(vipdocDir); err != nil {
		return err
	}

	// Parse ClickHouse HTTP URL from dbURI for raw queries
	u, err := url.Parse(dbURI)
	if err != nil {
		return fmt.Errorf("parse dbURI: %w", err)
	}
	httpPort := u.Query().Get("http_port")
	if httpPort == "" {
		httpPort = "8123"
	}
	chURL := fmt.Sprintf("http://%s:%s/", u.Hostname(), httpPort)
	chUser := u.User.Username()
	chPass, _ := u.User.Password()
	chDB := strings.TrimPrefix(u.Path, "/")
	if chDB == "" {
		chDB = "default"
	}

	chQuery := func(sql string) (string, error) {
		req, _ := http.NewRequestWithContext(ctx, "POST", chURL, strings.NewReader(sql))
		if chUser != "" {
			req.SetBasicAuth(chUser, chPass)
		}
		q := req.URL.Query()
		q.Set("database", chDB)
		req.URL.RawQuery = q.Encode()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return "", fmt.Errorf("CH error %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return string(body), nil
	}

	fmt.Printf("📦 扫描日线目录: %s\n", vipdocDir)

	// Step 1: Generate full CSV from .day files
	stockCSV := filepath.Join(TempDir, "stock_fill.csv")
	fmt.Println("🐌 转换 .day 文件为 CSV ...")
	if _, err := tdx.ConvertFilesToCSV(ctx, vipdocDir, stockCSV, ".day"); err != nil {
		return fmt.Errorf("convert day files: %w", err)
	}
	fi, _ := os.Stat(stockCSV)
	fmt.Printf("📄 CSV 大小: %.1f MB\n", float64(fi.Size())/1024/1024)

	// Step 2: Create fill table (same schema as raw_kline_daily)
	fmt.Println("📋 创建临时表 ...")
	if _, err := chQuery(fmt.Sprintf(
		"DROP TABLE IF EXISTS %s", fillTable,
	)); err != nil {
		return err
	}
	if _, err := chQuery(fmt.Sprintf(
		"CREATE TABLE %s AS raw_kline_daily", fillTable,
	)); err != nil {
		return fmt.Errorf("create fill table: %w", err)
	}

	// Step 3: Import full CSV into fill table
	fmt.Println("📥 导入 CSV 到临时表 ...")
	t0 := time.Now()
	if err := importCSVtoCH(ctx, chURL, chUser, chPass, chDB, fillTable, stockCSV); err != nil {
		return fmt.Errorf("import to fill table: %w", err)
	}
	fmt.Printf("   (耗时 %v)\n", time.Since(t0).Round(time.Second))

	// Step 4: Count missing rows
	result, err := chQuery(fmt.Sprintf(
		"SELECT count() FROM %s f LEFT ANTI JOIN raw_kline_daily d ON f.symbol = d.symbol AND f.date = d.date",
		fillTable,
	))
	if err != nil {
		return err
	}
	fmt.Printf("🔍 缺失行数: %s", strings.TrimSpace(result))

	missingCount := strings.TrimSpace(result)
	if missingCount == "0" {
		fmt.Println("\n✅ 日线数据全部已存在，跳过插入")
	} else {
		fmt.Println()

		// Step 5: Insert only missing rows
		fmt.Println("📥 插入缺失数据 ...")
		t0 = time.Now()
		if _, err := chQuery(fmt.Sprintf(
			"INSERT INTO raw_kline_daily SELECT f.* FROM %s f LEFT ANTI JOIN raw_kline_daily d ON f.symbol = d.symbol AND f.date = d.date",
			fillTable,
		)); err != nil {
			return fmt.Errorf("insert missing: %w", err)
		}
		fmt.Printf("   (耗时 %v)\n", time.Since(t0).Round(time.Second))

		// Step 6: Rebuild symbol_class
		fmt.Println("🏷️  重建 symbol_class ...")
		if err := db.RebuildSymbolClass(); err != nil {
			return fmt.Errorf("rebuild symbol_class: %w", err)
		}
	}

	// Step 7: Recalculate adjust factors
	fmt.Println("🔢 重算复权因子 ...")
	factorCSV := filepath.Join(TempDir, "factor_fill.csv")
	factorCount, err := calc.ExportFactorsToCSV(ctx, db, factorCSV, true)
	if err != nil {
		// Factor calculation failure shouldn't block data fill
		fmt.Printf("⚠️  复权因子计算失败: %v (日线数据仍然可用)\n", err)
	} else {
		fmt.Printf("   生成 %d 条复权因子\n", factorCount)
		if err := db.TruncateTable(model.TableAdjustFactor); err != nil {
			fmt.Printf("⚠️  清空复权因子表失败: %v\n", err)
		} else {
			if err := db.ImportAdjustFactors(factorCSV); err != nil {
				fmt.Printf("⚠️  导入复权因子失败: %v\n", err)
			} else {
				fmt.Println("   ✅ 复权因子更新完成")
			}
		}
	}

	// Step 8: Cleanup
	if _, err := chQuery(fmt.Sprintf("DROP TABLE %s", fillTable)); err != nil {
		return err
	}

	fmt.Println("✅ 日线补全完成")
	return nil
}

// importCSVtoCH imports a CSV file into ClickHouse via HTTP
func importCSVtoCH(ctx context.Context, chURL, user, pass, db, table, csvPath string) error {
	file, err := os.Open(csvPath)
	if err != nil {
		return err
	}
	defer file.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", chURL, file)
	if err != nil {
		return err
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	req.Header.Set("Content-Type", "text/csv")

	q := req.URL.Query()
	q.Set("database", db)
	q.Set("query", fmt.Sprintf("INSERT INTO %s FORMAT CSVWithNames", table))
	q.Set("date_time_input_format", "best_effort")
	q.Set("session_timezone", "Asia/Shanghai")
	req.URL.RawQuery = q.Encode()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("import failed %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
