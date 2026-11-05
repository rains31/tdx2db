package workflow

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/tdx"
	"github.com/jing2uo/tdx2db/utils"
)

// ExtraDailyValidDates 是 update_daily 写入 args.Extra 的 key
const ExtraDailyValidDates = "daily_valid_dates"

var (
	TaskUpdateDaily *Task
	TaskInitDaily   *Task
)

func init() {
	TaskUpdateDaily = &Task{
		Name:      "update_daily",
		DependsOn: []string{},
		SkipIf: func(ctx context.Context, db database.DataRepository, args *TaskArgs) bool {
			if !args.Day {
				return true
			}
			if args.Plan != nil && !args.Plan.NeedDaily {
				return true
			}
			return false
		},
		Executor:  executeUpdateDaily,
	}
	registerTask(TaskUpdateDaily, "update")

	TaskInitDaily = &Task{
		Name:      "init_daily",
		DependsOn: []string{},
		Executor:  executeInitDaily,
	}
	registerTask(TaskInitDaily, "init")
}

func executeUpdateDaily(ctx context.Context, db database.DataRepository, args *TaskArgs) (*TaskResult, error) {
	latestDate, err := db.GetLatestDate(model.TableKlineDaily.TableName, "date")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest date from database: %w", err)
	}
	fmt.Printf("📅 日线数据最新日期为 %s\n", latestDate.Format("2006-01-02"))

	src := pullSource{
		targetDir:   filepath.Join(args.VipdocDir, "refmhq"),
		urlTemplate: "https://www.tdx.com.cn/products/data/data/g4day/%s.zip",
		fileSuffix:  "day",
		label:       "日线",
	}
	validDates, err := pullDateRange(ctx, latestDate, src, args)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch daily data: %w", err)
	}

	if len(validDates) == 0 {
		fmt.Println("🌲 日线数据无需更新")
		return &TaskResult{State: StateSkipped, Message: "no new daily data"}, nil
	}

	if args.Extra == nil {
		args.Extra = map[string]interface{}{}
	}
	args.Extra[ExtraDailyValidDates] = validDates

	// Delete 模式：先删除当天的日线数据
	if args.Delete && args.TargetDate != "" {
		t, _ := time.Parse("20060102", args.TargetDate)
		dateStr := t.Format("2006-01-02")
		fmt.Printf("🗑️  先清后导 (--delete): 删除 %s 日线...\n", dateStr)
		if err := db.DeleteWhere(model.TableKlineDaily.TableName,
			fmt.Sprintf("date = '%s'", dateStr)); err != nil {
			return nil, fmt.Errorf("delete daily for delete flag: %w", err)
		}
		for i := 0; i < 30; i++ {
			exists, _ := db.Exists(model.TableKlineDaily.TableName, fmt.Sprintf("date = '%s'", dateStr))
			if !exists {
				break
			}
			time.Sleep(time.Second)
		}
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	endDate := validDates[len(validDates)-1]
	if err := tdx.DatatoolCreate(args.TempDir, args.VipdocDir, "day", endDate); err != nil {
		return nil, fmt.Errorf("failed to run DatatoolDayCreate: %w", err)
	}


	return executeDailyImport(ctx, db, args, args.VipdocDir)
}

func executeInitDaily(ctx context.Context, db database.DataRepository, args *TaskArgs) (*TaskResult, error) {
	fmt.Printf("📦 开始处理日线目录: %s\n", args.DayFileDir)
	if err := utils.CheckDirectory(args.DayFileDir); err != nil {
		return nil, err
	}

	return executeDailyImport(ctx, db, args, args.DayFileDir)
}

func executeDailyImport(ctx context.Context, db database.DataRepository, args *TaskArgs, sourceDir string) (*TaskResult, error) {
	// Delete 模式：先删除当天的日线数据
	if args.Delete && args.TargetDate != "" {
		t, _ := time.Parse("20060102", args.TargetDate)
		dateStr := t.Format("2006-01-02")
		fmt.Printf("🗑️  先清后导 (--delete): 删除 %s 日线...\n", dateStr)
		if err := db.DeleteWhere(model.TableKlineDaily.TableName,
			fmt.Sprintf("date = '%s'", dateStr)); err != nil {
			return nil, fmt.Errorf("delete daily for delete flag: %w", err)
		}
		// 等待 ClickHouse mutation 完成
		for i := 0; i < 30; i++ {
			exists, _ := db.Exists(model.TableKlineDaily.TableName, fmt.Sprintf("date = '%s'", dateStr))
			if !exists {
				break
			}
			time.Sleep(time.Second)
		}
	}

	fmt.Println("🐌 开始转换日线数据")

	stockDailyCSV := filepath.Join(args.TempDir, "stock.csv")

	_, err := tdx.ConvertFilesToCSV(ctx, sourceDir, stockDailyCSV, ".day")
	if err != nil {
		return nil, fmt.Errorf("failed to convert day files to csv: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// 方案 A: 若未传 --delete 且开启了 missing-only (默认开启)，通过临时表在库内进行差集比对，仅插入缺失数据
	if args.Delete {
		if err := db.ImportKlineDaily(stockDailyCSV); err != nil {
			return nil, fmt.Errorf("failed to import stock csv: %w", err)
		}
		fmt.Println("🚀 股票数据全量导入成功")
	} else if args.MissingOnly {
		n, err := db.ImportKlineDailyMissing(stockDailyCSV)
		if err != nil {
			return nil, fmt.Errorf("failed to import missing daily data: %w", err)
		}
		if n == 0 {
			fmt.Println("📊 日线数据全部已存在，无需补全")
		} else {
			fmt.Printf("📥 库内差集去重完成，成功补全 %d 行日线数据\n", n)
		}
	} else {
		if err := db.ImportKlineDaily(stockDailyCSV); err != nil {
			return nil, fmt.Errorf("failed to import stock csv: %w", err)
		}
		fmt.Println("🚀 股票数据全量导入成功 (--missing-only=false)")
	}

	if err := db.RebuildSymbolClass(); err != nil {
		return nil, fmt.Errorf("failed to rebuild symbol_class: %w", err)
	}

	fmt.Println("🚀 股票数据处理成功")
	return &TaskResult{State: StateCompleted, Message: "daily data imported"}, nil
}
