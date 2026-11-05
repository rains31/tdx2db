package duckdb

import (
	"fmt"
	"strings"
	"time"

	"github.com/jing2uo/tdx2db/model"
)

func (d *DuckDBDriver) ImportCSV(meta *model.TableMeta, csvPath string) error {
	var colMaps []string
	for _, col := range meta.Columns {
		duckType := d.mapType(col.Type)
		colMaps = append(colMaps, fmt.Sprintf("'%s': '%s'", col.Name, duckType))
	}

	columnsStr := strings.Join(colMaps, ", ")

	query := fmt.Sprintf(`
		INSERT INTO %s
		SELECT * FROM read_csv('%s',
			header=true,
			columns={%s},
			dateformat='%%Y-%%m-%%d',
			timestampformat='%%Y-%%m-%%d %%H:%%M'
		)
	`, meta.TableName, csvPath, columnsStr)

	_, err := d.db.Exec(query)
	return err
}

func (d *DuckDBDriver) TruncateTable(meta *model.TableMeta) error {
	query := fmt.Sprintf("DELETE FROM %s", meta.TableName)
	if _, err := d.db.Exec(query); err != nil {
		return fmt.Errorf("duckdb truncate failed: %w", err)
	}
	return nil
}

func (d *DuckDBDriver) ImportKlineDaily(path string) error {
	return d.ImportCSV(model.TableKlineDaily, path)
}

// ImportKlineDailyMissing imports daily CSV via a temp table and inserts only missing rows.
func (d *DuckDBDriver) ImportKlineDailyMissing(path string) (int64, error) {
	tmpTable := fmt.Sprintf("_tmp_kline_daily_%d", time.Now().UnixNano())
	createSQL := fmt.Sprintf("CREATE TEMP TABLE %s AS SELECT * FROM %s LIMIT 0", tmpTable, model.TableKlineDaily.TableName)
	if _, err := d.db.Exec(createSQL); err != nil {
		return 0, fmt.Errorf("create duckdb daily temp table: %w", err)
	}
	defer d.db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", tmpTable))

	copySQL := fmt.Sprintf("COPY %s FROM '%s' (HEADER, DELIMITER ',')", tmpTable, path)
	if _, err := d.db.Exec(copySQL); err != nil {
		return 0, fmt.Errorf("copy daily to temp table: %w", err)
	}

	countSQL := fmt.Sprintf(`
		SELECT count(*) FROM %s t
		ANTI JOIN %s d ON t.symbol = d.symbol AND t.date = d.date
	`, tmpTable, model.TableKlineDaily.TableName)

	var missingRows int64
	if err := d.db.QueryRow(countSQL).Scan(&missingRows); err != nil {
		return 0, fmt.Errorf("count missing daily rows: %w", err)
	}

	if missingRows == 0 {
		return 0, nil
	}

	insertSQL := fmt.Sprintf(`
		INSERT INTO %s
		SELECT t.* FROM %s t
		ANTI JOIN %s d ON t.symbol = d.symbol AND t.date = d.date
	`, model.TableKlineDaily.TableName, tmpTable, model.TableKlineDaily.TableName)

	if _, err := d.db.Exec(insertSQL); err != nil {
		return 0, fmt.Errorf("insert missing daily rows: %w", err)
	}

	return missingRows, nil
}

func (d *DuckDBDriver) ImportKline1Min(path string) error {
	return d.ImportCSV(model.TableKline1Min, path)
}

// ImportKline1MinMissing imports 1-minute CSV via a temp table and inserts only missing rows.
func (d *DuckDBDriver) ImportKline1MinMissing(path string) (int64, error) {
	tmpTable := fmt.Sprintf("_tmp_kline_1min_%d", time.Now().UnixNano())
	createSQL := fmt.Sprintf("CREATE TEMP TABLE %s AS SELECT * FROM %s LIMIT 0", tmpTable, model.TableKline1Min.TableName)
	if _, err := d.db.Exec(createSQL); err != nil {
		return 0, fmt.Errorf("create duckdb 1min temp table: %w", err)
	}
	defer d.db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", tmpTable))

	copySQL := fmt.Sprintf("COPY %s FROM '%s' (HEADER, DELIMITER ',')", tmpTable, path)
	if _, err := d.db.Exec(copySQL); err != nil {
		return 0, fmt.Errorf("copy 1min to temp table: %w", err)
	}

	countSQL := fmt.Sprintf(`
		SELECT count(*) FROM %s t
		ANTI JOIN %s m ON t.symbol = m.symbol AND t.datetime = m.datetime
	`, tmpTable, model.TableKline1Min.TableName)

	var missingRows int64
	if err := d.db.QueryRow(countSQL).Scan(&missingRows); err != nil {
		return 0, fmt.Errorf("count missing 1min rows: %w", err)
	}

	if missingRows == 0 {
		return 0, nil
	}

	insertSQL := fmt.Sprintf(`
		INSERT INTO %s
		SELECT t.* FROM %s t
		ANTI JOIN %s m ON t.symbol = m.symbol AND t.datetime = m.datetime
	`, model.TableKline1Min.TableName, tmpTable, model.TableKline1Min.TableName)

	if _, err := d.db.Exec(insertSQL); err != nil {
		return 0, fmt.Errorf("insert missing 1min rows: %w", err)
	}

	return missingRows, nil
}

// QueryExisting1Min returns existing (date:symbol:minute) pairs as "date|symbol|HH:MM" keys
func (d *DuckDBDriver) QueryExisting1Min(date time.Time) (map[string]bool, error) {
	dateStr := date.Format("2006-01-02")
	nextDateStr := date.AddDate(0, 0, 1).Format("2006-01-02")
	query := fmt.Sprintf(
		"SELECT DISTINCT symbol, strftime(datetime, '%%H:%%M') FROM %s WHERE datetime >= '%s' AND datetime < '%s'",
		model.TableKline1Min.TableName, dateStr, nextDateStr,
	)
	rows, err := d.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var symbol, minStr string
		if err := rows.Scan(&symbol, &minStr); err != nil {
			return nil, err
		}
		result[dateStr+"|"+symbol+"|"+minStr] = true
	}
	return result, nil
}

// QueryExistingDaily returns existing (date, symbol) pairs as "date|symbol" keys
func (d *DuckDBDriver) QueryExistingDaily(dates []time.Time) (map[string]bool, error) {
	if len(dates) == 0 {
		return make(map[string]bool), nil
	}
	var dateStrs []string
	for _, dt := range dates {
		dateStrs = append(dateStrs, fmt.Sprintf("'%s'", dt.Format("2006-01-02")))
	}
	query := fmt.Sprintf(
		"SELECT DISTINCT symbol, strftime(date, '%%Y-%%m-%%d') FROM %s WHERE date IN (%s)",
		model.TableKlineDaily.TableName, strings.Join(dateStrs, ","),
	)
	rows, err := d.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var symbol, dateStr string
		if err := rows.Scan(&symbol, &dateStr); err != nil {
			return nil, err
		}
		result[dateStr+"|"+symbol] = true
	}
	return result, nil
}

func (d *DuckDBDriver) ImportGBBQ(path string) error {
	d.TruncateTable(model.TableGbbq)
	return d.ImportCSV(model.TableGbbq, path)
}

func (d *DuckDBDriver) ImportBasic(path string) error {
	return d.ImportCSV(model.TableBasicDaily, path)
}

func (d *DuckDBDriver) ImportAdjustFactors(path string) error {
	return d.ImportCSV(model.TableAdjustFactor, path)
}

func (d *DuckDBDriver) ImportHolidays(path string) error {
	d.TruncateTable(model.TableHoliday)
	return d.ImportCSV(model.TableHoliday, path)
}

func (d *DuckDBDriver) ImportBlockInfo(path string) error {
	d.TruncateTable(model.TableBlockInfo)
	return d.ImportCSV(model.TableBlockInfo, path)
}

func (d *DuckDBDriver) ImportBlockMembers(path string) error {
	d.TruncateTable(model.TableBlockMember)
	return d.ImportCSV(model.TableBlockMember, path)
}

func (d *DuckDBDriver) ImportSymbolNames(path string) error {
	d.TruncateTable(model.TableSymbolName)
	return d.ImportCSV(model.TableSymbolName, path)
}

// RebuildSymbolClass 全量重建 symbol_class 表（从 raw_kline_daily 取 distinct symbol 后归类）。
// 写入逻辑跟 Import 系列同性质，放这里。
func (d *DuckDBDriver) RebuildSymbolClass() error {
	kline := model.TableKlineDaily.TableName
	class := model.TableSymbolClass.TableName

	var codes []string
	if err := d.db.Select(&codes, fmt.Sprintf("SELECT DISTINCT symbol FROM %s", kline)); err != nil {
		return fmt.Errorf("failed to collect symbols: %w", err)
	}

	tx, err := d.db.Beginx()
	if err != nil {
		return fmt.Errorf("failed to begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(fmt.Sprintf("DELETE FROM %s", class)); err != nil {
		return fmt.Errorf("failed to clear symbol_class: %w", err)
	}

	stmt, err := tx.Preparex(fmt.Sprintf("INSERT INTO %s (symbol, class) VALUES (?, ?)", class))
	if err != nil {
		return fmt.Errorf("failed to prepare insert: %w", err)
	}
	defer stmt.Close()

	for _, c := range codes {
		if _, err := stmt.Exec(c, model.ClassifyCode(c)); err != nil {
			return fmt.Errorf("failed to insert class for %s: %w", c, err)
		}
	}

	return tx.Commit()
}
