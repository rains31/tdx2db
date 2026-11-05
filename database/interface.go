package database

import (
	"time"

	"github.com/jing2uo/tdx2db/model"
)

type DataRepository interface {
	Connect() error
	Close() error

	InitSchema() error

	ReadSchemaVersion() (string, error)
	WriteSchemaVersion() error

	ImportCSV(meta *model.TableMeta, csvPath string) error
	ImportKlineDaily(csvPath string) error
	ImportKlineDailyMissing(csvPath string) (int64, error)
	ImportKline1Min(csvPath string) error
	ImportKline1MinMissing(csvPath string) (int64, error)
	ImportAdjustFactors(csvPath string) error
	ImportGBBQ(csvPath string) error
	ImportBasic(csvPath string) error
	ImportHolidays(csvPath string) error
	ImportBlockInfo(csvPath string) error
	ImportBlockMembers(csvPath string) error
	ImportSymbolNames(csvPath string) error

	TruncateTable(meta *model.TableMeta) error
	Query(table string, conditions map[string]interface{}, dest interface{}) error
	QueryKlineDaily(symbol string, startDate, endDate *time.Time) ([]model.KlineDay, error)
	GetLatestDate(tableName string, dateCol string) (time.Time, error)
	GetMinDate(tableName string, dateCol string) (time.Time, error)
	GetSymbolsByClass(classes ...string) ([]string, error)
	RebuildSymbolClass() error
	CountKlineDaily() (int64, error)

	GetBasicsBySymbol(symbol string) ([]model.BasicDaily, error)

	GetGbbq() ([]model.GbbqData, error)
	GetHolidays() ([]time.Time, error)

	Exists(table string, where string, args ...interface{}) (bool, error)

	// QueryExisting1Min returns symbols that already have 1min data on the given date
	QueryExisting1Min(date time.Time) (map[string]bool, error)

	// QueryExistingDaily returns existing (date, symbol) pairs as "date|symbol" keys
	QueryExistingDaily(dates []time.Time) (map[string]bool, error)

	// DeleteWhere deletes rows matching a WHERE clause (for force mode)
	DeleteWhere(table string, where string, args ...interface{}) error
}

