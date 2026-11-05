package workflow

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/tdx"
)

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

	// 方案 A: 若未传 --delete 且开启了 missing-only (默认开启)，通过临时表在库内进行差集比对，仅插入缺失分钟线
	if args.Delete {
		if err := db.ImportKline1Min(stock1MinCSV); err != nil {
			return nil, fmt.Errorf("failed to import 1-minute line csv: %w", err)
		}
		fmt.Println("📊 分时数据全量导入成功")
	} else if args.MissingOnly {
		n, err := db.ImportKline1MinMissing(stock1MinCSV)
		if err != nil {
			return nil, fmt.Errorf("failed to import missing 1min data: %w", err)
		}
		if n == 0 {
			fmt.Println("📊 分时数据全部已存在，无需补全")
		} else {
			fmt.Printf("📥 库内差集去重完成，成功补全 %d 行分时数据\n", n)
		}
	} else {
		if err := db.ImportKline1Min(stock1MinCSV); err != nil {
			return nil, fmt.Errorf("failed to import 1-minute line csv: %w", err)
		}
		fmt.Println("📊 分时数据全量导入成功 (--missing-only=false)")
	}

	return &TaskResult{State: StateCompleted, Message: "1min data imported"}, nil
}
