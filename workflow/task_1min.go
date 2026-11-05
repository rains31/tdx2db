package workflow

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/tdx"
)

// datetimeFormats 是 CSV datetime 字段可能的格式列表，按优先级排序。
// CSVWriter 默认用 time.RFC3339（"2006-01-02T15:04:05Z07:00"），
// 兼容旧版可能使用的空格分隔格式。
var datetimeFormats = []string{
	time.RFC3339,           // 2006-01-02T15:04:05+08:00
	"2006-01-02 15:04:05", // 旧格式兼容
}

var TaskUpdate1Min *Task

func init() {
	TaskUpdate1Min = &Task{
		Name:      "update_1min",
		DependsOn: []string{"prepare_tic"},
		SkipIf: func(ctx context.Context, db database.DataRepository, args *TaskArgs) bool {
			return !args.Min
		},
		Executor: executeUpdate1Min,
		OnError:  ErrorModeSkip,
	}
	registerTask(TaskUpdate1Min, "update")
}

func executeUpdate1Min(ctx context.Context, db database.DataRepository, args *TaskArgs) (*TaskResult, error) {
	validDates, _ := args.Extra[ExtraTicValidDates].([]time.Time)
	if len(validDates) == 0 {
		fmt.Println("🌲 分时数据无需更新")
		return &TaskResult{State: StateSkipped, Message: "no new 1min data"}, nil
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	endDate := validDates[len(validDates)-1]
	fmt.Printf("🐌 开始转换分钟数据\n")
	if err := tdx.DatatoolCreate(args.TempDir, args.VipdocDir, "min", endDate); err != nil {
		return nil, fmt.Errorf("failed to run DatatoolMinCreate: %w", err)
	}


	fmt.Printf("🐌 开始转换分时数据\n")
	stock1MinCSV := filepath.Join(args.TempDir, "1min.csv")
	if _, err := tdx.ConvertFilesToCSV(ctx, args.VipdocDir, stock1MinCSV, ".01"); err != nil {
		return nil, fmt.Errorf("failed to convert .01 files to csv: %w", err)
	}

	if args.Delete && args.TargetDate != "" {
		t, _ := time.Parse("20060102", args.TargetDate)
		dateStr := t.Format("2006-01-02")
		nextStr := t.AddDate(0, 0, 1).Format("2006-01-02")
		fmt.Printf("🗑️  先清后导 (--delete): 删除 %s 分钟线...\n", dateStr)
		if err := db.DeleteWhere(model.TableKline1Min.TableName,
			fmt.Sprintf("datetime >= '%s' AND datetime < '%s'", dateStr, nextStr)); err != nil {
			return nil, fmt.Errorf("delete 1min for delete flag: %w", err)
		}
		// 等待 ClickHouse mutation 完成
		for i := 0; i < 30; i++ {
			exists, _ := db.Exists(model.TableKline1Min.TableName,
				fmt.Sprintf("datetime >= '%s' AND datetime < '%s'", dateStr, nextStr))
			if !exists {
				break
			}
			time.Sleep(time.Second)
		}
	}

	if args.MissingOnly {
		filteredCSV := filepath.Join(args.TempDir, "1min_filtered.csv")
		n, err := filterMissingFromCSV(db, stock1MinCSV, filteredCSV, validDates)
		if err != nil {
			return nil, fmt.Errorf("failed to filter missing data: %w", err)
		}
		if n == 0 {
			fmt.Println("📊 全部数据已存在，无需补全")
			return &TaskResult{State: StateCompleted, Message: "all data already exists"}, nil
		}
		fmt.Printf("🔍 过滤后剩余 %d 行待补全\n", n)
		stock1MinCSV = filteredCSV
	}

	if err := db.ImportKline1Min(stock1MinCSV); err != nil {
		return nil, fmt.Errorf("failed to import 1-minute line csv: %w", err)
	}
	fmt.Println("📊 分时数据导入成功")
	return &TaskResult{State: StateCompleted, Message: "1min data imported"}, nil
}

// filterMissingFromCSV reads a CSV (with header "symbol,...datetime,..."),
// queries ClickHouse for existing (date,symbol,minute) triplets across validDates,
// and writes only rows whose (date,symbol,minute) are not found AND whose date is in validDates.
// Returns the number of rows written.
func filterMissingFromCSV(db database.DataRepository, inputCSV, outputCSV string, validDates []time.Time) (int, error) {
	// Build valid date set (YYYY-MM-DD strings) — only rows in this set will be written
	validDateSet := make(map[string]bool, len(validDates))
	for _, d := range validDates {
		validDateSet[d.Format("2006-01-02")] = true
	}

	// Build existing set: "date|symbol|HH:MM" keys for all valid dates
	existing := make(map[string]bool)
	for _, d := range validDates {
		syms, err := db.QueryExisting1Min(d)
		if err != nil {
			return 0, fmt.Errorf("query existing 1min for %s: %w", d.Format("2006-01-02"), err)
		}
		for s := range syms {
			existing[s] = true
		}
	}

	in, err := os.Open(inputCSV)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	out, err := os.Create(outputCSV)
	if err != nil {
		return 0, err
	}
	defer out.Close()

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	wrote := 0
	firstLine := true
	for scanner.Scan() {
		line := scanner.Text()
		if firstLine {
			fmt.Fprintln(out, line)
			firstLine = false
			continue
		}

		// CSV format: symbol,open,high,low,close,amount,volume,datetime
		comma := strings.IndexByte(line, ',')
		if comma < 0 {
			continue
		}
		symbol := line[:comma]

		// datetime is the last column
		lastComma := strings.LastIndexByte(line, ',')
		if lastComma < 0 || lastComma <= comma {
			continue
		}
		datetimeRaw := strings.Trim(line[lastComma+1:], "\"")

		// CSVWriter 使用 time.RFC3339 序列化 datetime（如 "2026-06-29T09:31:00+08:00"）
		// 用 time.Parse 解析，不依赖字符串偏移，兼容多种格式
		var t time.Time
		var parsed bool
		for _, layout := range datetimeFormats {
			if pt, err := time.Parse(layout, datetimeRaw); err == nil {
				t = pt
				parsed = true
				break
			}
		}
		if !parsed {
			continue
		}

		dateStr := t.Format("2006-01-02") // "2026-06-29"
		minStr := t.Format("15:04")       // "09:31"

		// 只处理目标日期范围内的行，拒绝无关历史数据
		if !validDateSet[dateStr] {
			continue
		}

		// key 格式: "date|symbol|HH:MM"，与 QueryExisting1Min 保持一致
		key := dateStr + "|" + symbol + "|" + minStr
		if existing[key] {
			continue // skip — this (date, symbol, minute) already exists
		}
		fmt.Fprintln(out, line)
		wrote++
	}

	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return wrote, nil
}
