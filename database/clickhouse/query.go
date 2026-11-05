package clickhouse

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jing2uo/tdx2db/model"
)

func (d *ClickHouseDriver) Query(table string, conditions map[string]interface{}, dest interface{}) error {
	query := fmt.Sprintf("SELECT * FROM %s", table)
	args := []interface{}{}
	if len(conditions) > 0 {
		whereParts := []string{}
		for k, v := range conditions {
			whereParts = append(whereParts, fmt.Sprintf("%s = ?", k))
			args = append(args, v)
		}
		query += " WHERE " + strings.Join(whereParts, " AND ")
	}

	return d.db.Select(dest, query, args...)
}

func (d *ClickHouseDriver) GetLatestDate(tableName string, dateCol string) (time.Time, error) {
	query := fmt.Sprintf("SELECT toDate(maxOrNull(%s)) AS latest FROM %s", dateCol, tableName)
	var latest sql.NullTime
	err := d.db.Get(&latest, query)
	if err != nil {
		return time.Time{}, err
	}
	if !latest.Valid {
		return time.Time{}, nil
	}
	return latest.Time, nil
}

func (d *ClickHouseDriver) GetMinDate(tableName string, dateCol string) (time.Time, error) {
	query := fmt.Sprintf("SELECT toDate(minOrNull(%s)) AS earliest FROM %s", dateCol, tableName)
	var earliest sql.NullTime
	err := d.db.Get(&earliest, query)
	if err != nil {
		return time.Time{}, err
	}
	if !earliest.Valid {
		return time.Time{}, nil
	}
	return earliest.Time, nil
}

// DeleteWhere deletes rows matching a WHERE clause (for force mode).
// Note: ClickHouse ALTER TABLE DELETE does not support parameterized placeholders,
// so where must already have values inlined (e.g. "date = '2026-07-03'").
func (d *ClickHouseDriver) DeleteWhere(tableName string, where string, args ...interface{}) error {
	query := fmt.Sprintf("ALTER TABLE %s DELETE WHERE %s", tableName, where)
	// ClickHouse DELETE uses mutations — use raw query via HTTP for reliability
	req, _ := http.NewRequest("POST", d.httpImportUrl, strings.NewReader(query))
	if d.authUser != "" {
		req.SetBasicAuth(d.authUser, d.authPass)
	}
	q := req.URL.Query()
	q.Set("database", d.database)
	req.URL.RawQuery = q.Encode()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete failed %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (d *ClickHouseDriver) GetSymbolsByClass(classes ...string) ([]string, error) {
	if len(classes) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(classes))
	placeholders = placeholders[:len(placeholders)-1]
	query := fmt.Sprintf(
		"SELECT symbol FROM %s WHERE class IN (%s) ORDER BY symbol",
		model.TableSymbolClass.TableName, placeholders,
	)
	args := make([]any, len(classes))
	for i, c := range classes {
		args[i] = c
	}

	var symbols []string
	if err := d.db.Select(&symbols, query, args...); err != nil {
		return nil, fmt.Errorf("failed to query symbols by class: %w", err)
	}
	return symbols, nil
}

func (d *ClickHouseDriver) CountKlineDaily() (int64, error) {
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", model.TableKlineDaily.TableName)

	var count int64
	err := d.db.Get(&count, query)
	if err != nil {
		return 0, fmt.Errorf("failed to count kline daily: %w", err)
	}

	return count, nil
}

func (d *ClickHouseDriver) QueryKlineDaily(symbol string, startDate, endDate *time.Time) ([]model.KlineDay, error) {
	conditions := []string{"symbol = ?"}
	args := []interface{}{symbol}

	if startDate != nil {
		conditions = append(conditions, "date >= ?")
		args = append(args, *startDate)
	}
	if endDate != nil {
		conditions = append(conditions, "date <= ?")
		args = append(args, *endDate)
	}

	query := fmt.Sprintf(
		`SELECT * FROM %s WHERE %s ORDER BY date ASC`,
		model.TableKlineDaily.TableName,
		strings.Join(conditions, " AND "),
	)

	var results []model.KlineDay
	if err := d.db.Select(&results, query, args...); err != nil {
		return nil, fmt.Errorf("failed to query kline daily: %w", err)
	}

	return results, nil
}

func (d *ClickHouseDriver) GetBasicsBySymbol(symbol string) ([]model.BasicDaily, error) {
	query := fmt.Sprintf(
		"SELECT * FROM %s WHERE symbol = ? ORDER BY date",
		model.TableBasicDaily.TableName,
	)

	var results []model.BasicDaily
	if err := d.db.Select(&results, query, symbol); err != nil {
		return nil, fmt.Errorf("failed to query daily basics by symbol %s: %w", symbol, err)
	}

	return results, nil
}

func (d *ClickHouseDriver) GetGbbq() ([]model.GbbqData, error) {
	table := model.TableGbbq.TableName

	query := fmt.Sprintf(`SELECT * FROM %s ORDER BY symbol, date`, table)

	var results []model.GbbqData
	if err := d.db.Select(&results, query); err != nil {
		return nil, fmt.Errorf("failed to query gbbq: %w", err)
	}

	return results, nil
}

func (d *ClickHouseDriver) GetHolidays() ([]time.Time, error) {
	query := fmt.Sprintf("SELECT date FROM %s ORDER BY date", model.TableHoliday.TableName)
	var dates []time.Time
	if err := d.db.Select(&dates, query); err != nil {
		return nil, fmt.Errorf("failed to query holidays: %w", err)
	}
	return dates, nil
}

func (d *ClickHouseDriver) Exists(table string, where string, args ...interface{}) (bool, error) {
	query := fmt.Sprintf("SELECT 1 FROM %s", table)
	if where != "" {
		query += " WHERE " + where
	}
	query += " LIMIT 1"

	var dummy int
	err := d.db.Get(&dummy, query, args...)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		// ClickHouse might return an empty result set error rather than ErrNoRows
		if strings.Contains(err.Error(), "empty result") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

