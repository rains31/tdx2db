package clickhouse

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jing2uo/tdx2db/model"
)

func (d *ClickHouseDriver) importCSVToTableName(tableName string, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	req, err := http.NewRequest("POST", d.httpImportUrl, file)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "text/csv")

	// 设置参数
	q := req.URL.Query()

	if d.database != "" {
		q.Set("database", d.database)
	}

	q.Add("query", fmt.Sprintf("INSERT INTO %s FORMAT CSVWithNames", tableName))
	q.Add("date_time_input_format", "best_effort")
	q.Add("session_timezone", "Asia/Shanghai")

	req.URL.RawQuery = q.Encode()

	if d.authUser != "" {
		req.SetBasicAuth(d.authUser, d.authPass)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		errMsg := strings.TrimSpace(string(bodyBytes))
		return fmt.Errorf("clickhouse insert failed (table: %s, db: %s, status %d): %s", tableName, d.database, resp.StatusCode, errMsg)
	}

	return nil
}

func (d *ClickHouseDriver) ImportCSV(meta *model.TableMeta, filePath string) error {
	return d.importCSVToTableName(meta.TableName, filePath)
}

func (d *ClickHouseDriver) TruncateTable(meta *model.TableMeta) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	query := fmt.Sprintf("TRUNCATE TABLE IF EXISTS %s", meta.TableName)

	if _, err := d.db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("clickhouse truncate via tcp failed: %w", err)
	}

	return nil
}

func (d *ClickHouseDriver) ImportKlineDaily(path string) error {
	return d.ImportCSV(model.TableKlineDaily, path)
}

// ImportKlineDailyMissing imports daily CSV via a temporary table and inserts only missing (date, symbol) rows.
func (d *ClickHouseDriver) ImportKlineDailyMissing(path string) (int64, error) {
	tmpTable := fmt.Sprintf("_tmp_kline_daily_%d", time.Now().UnixNano())
	createSQL := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s AS %s", tmpTable, model.TableKlineDaily.TableName)
	if _, err := d.db.Exec(createSQL); err != nil {
		return 0, fmt.Errorf("create daily temp table: %w", err)
	}
	defer func() {
		d.db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", tmpTable))
	}()

	if err := d.importCSVToTableName(tmpTable, path); err != nil {
		return 0, fmt.Errorf("import daily to temp table: %w", err)
	}

	// 探测临时表中的时间跨度
	var minDateStr, maxDateStr string
	var totalRows int64
	row := d.db.QueryRow(fmt.Sprintf("SELECT toString(min(date)), toString(max(date)), count() FROM %s", tmpTable))
	if err := row.Scan(&minDateStr, &maxDateStr, &totalRows); err != nil {
		return 0, fmt.Errorf("query daily temp table range: %w", err)
	}
	if totalRows == 0 {
		return 0, nil
	}

	// 计算待插入行数
	countSQL := fmt.Sprintf(`
		SELECT count() FROM %s t
		LEFT ANTI JOIN (
			SELECT symbol, date FROM %s
			WHERE date >= '%s' AND date <= '%s'
		) d ON t.symbol = d.symbol AND t.date = d.date
	`, tmpTable, model.TableKlineDaily.TableName, minDateStr, maxDateStr)

	var missingRows int64
	if err := d.db.QueryRow(countSQL).Scan(&missingRows); err != nil {
		return 0, fmt.Errorf("count missing daily rows: %w", err)
	}

	if missingRows == 0 {
		return 0, nil
	}

	// 库内 LEFT ANTI JOIN 差集插入
	insertSQL := fmt.Sprintf(`
		INSERT INTO %s
		SELECT t.* FROM %s t
		LEFT ANTI JOIN (
			SELECT symbol, date FROM %s
			WHERE date >= '%s' AND date <= '%s'
		) d ON t.symbol = d.symbol AND t.date = d.date
	`, model.TableKlineDaily.TableName, tmpTable, model.TableKlineDaily.TableName, minDateStr, maxDateStr)

	if _, err := d.db.Exec(insertSQL); err != nil {
		return 0, fmt.Errorf("insert missing daily rows: %w", err)
	}

	return missingRows, nil
}

func (d *ClickHouseDriver) ImportKline1Min(path string) error {
	return d.ImportCSV(model.TableKline1Min, path)
}

// ImportKline1MinMissing imports 1-minute CSV via a temporary table and inserts only missing (datetime, symbol) rows.
func (d *ClickHouseDriver) ImportKline1MinMissing(path string) (int64, error) {
	tmpTable := fmt.Sprintf("_tmp_kline_1min_%d", time.Now().UnixNano())
	createSQL := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s AS %s", tmpTable, model.TableKline1Min.TableName)
	if _, err := d.db.Exec(createSQL); err != nil {
		return 0, fmt.Errorf("create 1min temp table: %w", err)
	}
	defer func() {
		d.db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", tmpTable))
	}()

	if err := d.importCSVToTableName(tmpTable, path); err != nil {
		return 0, fmt.Errorf("import 1min to temp table: %w", err)
	}

	// 探测临时表中的时间跨度
	var minDtStr, maxDtStr string
	var totalRows int64
	row := d.db.QueryRow(fmt.Sprintf("SELECT formatDateTime(min(datetime), '%%Y-%%m-%%d %%H:%%i:%%s'), formatDateTime(max(datetime), '%%Y-%%m-%%d %%H:%%i:%%s'), count() FROM %s", tmpTable))
	if err := row.Scan(&minDtStr, &maxDtStr, &totalRows); err != nil {
		return 0, fmt.Errorf("query 1min temp table range: %w", err)
	}
	if totalRows == 0 {
		return 0, nil
	}

	// 计算待插入行数
	countSQL := fmt.Sprintf(`
		SELECT count() FROM %s t
		LEFT ANTI JOIN (
			SELECT symbol, datetime FROM %s
			WHERE datetime >= '%s' AND datetime <= '%s'
		) m ON t.symbol = m.symbol AND t.datetime = m.datetime
	`, tmpTable, model.TableKline1Min.TableName, minDtStr, maxDtStr)

	var missingRows int64
	if err := d.db.QueryRow(countSQL).Scan(&missingRows); err != nil {
		return 0, fmt.Errorf("count missing 1min rows: %w", err)
	}

	if missingRows == 0 {
		return 0, nil
	}

	// 库内 LEFT ANTI JOIN 差集插入
	insertSQL := fmt.Sprintf(`
		INSERT INTO %s
		SELECT t.* FROM %s t
		LEFT ANTI JOIN (
			SELECT symbol, datetime FROM %s
			WHERE datetime >= '%s' AND datetime <= '%s'
		) m ON t.symbol = m.symbol AND t.datetime = m.datetime
	`, model.TableKline1Min.TableName, tmpTable, model.TableKline1Min.TableName, minDtStr, maxDtStr)

	if _, err := d.db.Exec(insertSQL); err != nil {
		return 0, fmt.Errorf("insert missing 1min rows: %w", err)
	}

	return missingRows, nil
}

// QueryExisting1Min returns existing (date:symbol:minute) pairs as "date|symbol|HH:MM" keys
func (d *ClickHouseDriver) QueryExisting1Min(date time.Time) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	dateStr := date.Format("2006-01-02")
	nextDateStr := date.AddDate(0, 0, 1).Format("2006-01-02")
	// 范围查询 datetime >= dateStr 00:00:00 AND datetime < nextDateStr 00:00:00 能直接走索引和分区
	query := fmt.Sprintf(
		"SELECT DISTINCT symbol, formatDateTime(datetime, '%%H:%%i') FROM %s WHERE datetime >= '%s 00:00:00' AND datetime < '%s 00:00:00'",
		model.TableKline1Min.TableName, dateStr, nextDateStr,
	)

	req, err := http.NewRequestWithContext(ctx, "POST", d.httpImportUrl, strings.NewReader(query))
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	if d.database != "" {
		q.Add("database", d.database)
	}
	req.URL.RawQuery = q.Encode()
	if d.authUser != "" {
		req.SetBasicAuth(d.authUser, d.authPass)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clickhouse query 1min failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	result := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		// TabSeparated: symbol\tHH:MM
		parts := strings.Split(line, "\t")
		if len(parts) >= 2 {
			// key 格式: "date|symbol|HH:MM"，含日期维度，避免跨日误判
			key := dateStr + "|" + strings.TrimSpace(parts[0]) + "|" + strings.TrimSpace(parts[1])
			result[key] = true
		}
	}
	return result, nil
}

// QueryExistingDaily returns existing (date, symbol) pairs as "date|symbol" keys
func (d *ClickHouseDriver) QueryExistingDaily(dates []time.Time) (map[string]bool, error) {
	if len(dates) == 0 {
		return make(map[string]bool), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var dateStrs []string
	for _, dt := range dates {
		dateStrs = append(dateStrs, fmt.Sprintf("'%s'", dt.Format("2006-01-02")))
	}
	query := fmt.Sprintf(
		"SELECT DISTINCT symbol, toString(date) FROM %s WHERE date IN (%s)",
		model.TableKlineDaily.TableName, strings.Join(dateStrs, ","),
	)

	req, err := http.NewRequestWithContext(ctx, "POST", d.httpImportUrl, strings.NewReader(query))
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	if d.database != "" {
		q.Add("database", d.database)
	}
	req.URL.RawQuery = q.Encode()
	if d.authUser != "" {
		req.SetBasicAuth(d.authUser, d.authPass)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clickhouse query daily failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	result := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		// TabSeparated: symbol\tYYYY-MM-DD
		parts := strings.Split(line, "\t")
		if len(parts) >= 2 {
			key := strings.TrimSpace(parts[1]) + "|" + strings.TrimSpace(parts[0])
			result[key] = true
		}
	}
	return result, nil
}

func (d *ClickHouseDriver) ImportGBBQ(path string) error {
	d.TruncateTable(model.TableGbbq)
	return d.ImportCSV(model.TableGbbq, path)
}

func (d *ClickHouseDriver) ImportBasic(path string) error {
	return d.ImportCSV(model.TableBasicDaily, path)
}

func (d *ClickHouseDriver) ImportAdjustFactors(path string) error {
	return d.ImportCSV(model.TableAdjustFactor, path)
}

func (d *ClickHouseDriver) ImportHolidays(path string) error {
	d.TruncateTable(model.TableHoliday)
	return d.ImportCSV(model.TableHoliday, path)
}

func (d *ClickHouseDriver) ImportBlockInfo(path string) error {
	if err := d.TruncateTable(model.TableBlockInfo); err != nil {
		return err
	}
	return d.ImportCSV(model.TableBlockInfo, path)
}

func (d *ClickHouseDriver) ImportBlockMembers(path string) error {
	if err := d.TruncateTable(model.TableBlockMember); err != nil {
		return err
	}
	return d.ImportCSV(model.TableBlockMember, path)
}

func (d *ClickHouseDriver) ImportSymbolNames(path string) error {
	if err := d.TruncateTable(model.TableSymbolName); err != nil {
		return err
	}
	return d.ImportCSV(model.TableSymbolName, path)
}

// RebuildSymbolClass 全量重建 symbol_class 表（从 raw_kline_daily 取 distinct symbol 后归类）。
// 写入逻辑跟 Import 系列同性质，放这里。
func (d *ClickHouseDriver) RebuildSymbolClass() error {
	kline := model.TableKlineDaily.TableName
	class := model.TableSymbolClass.TableName

	var codes []string
	if err := d.db.Select(&codes, fmt.Sprintf("SELECT DISTINCT symbol FROM %s", kline)); err != nil {
		return fmt.Errorf("failed to collect symbols: %w", err)
	}

	if err := d.TruncateTable(model.TableSymbolClass); err != nil {
		return err
	}

	if len(codes) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("INSERT INTO %s (symbol, class) VALUES ", class))
	for i, c := range codes {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(fmt.Sprintf("('%s', '%s')", c, model.ClassifyCode(c)))
	}

	if _, err := d.db.Exec(sb.String()); err != nil {
		return fmt.Errorf("failed to insert symbol_class: %w", err)
	}
	return nil
}
