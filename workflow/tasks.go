package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jing2uo/tdx2db/calc"
	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/tdx"
	"github.com/jing2uo/tdx2db/utils"
)

var (
	TaskUpdateDaily    *Task
	TaskInitDaily      *Task
	TaskUpdateGBBQ     *Task
	TaskCalcBasic      *Task
	TaskCalcFactor     *Task
	TaskUpdate1Min     *Task
	TaskUpdateHolidays *Task
)

func init() {
	TaskUpdateDaily = &Task{
		Name:      "update_daily",
		DependsOn: []string{},
		SkipIf:    skipIfPlan(func(p *WorkPlan) bool { return !p.NeedDaily }),
		Executor:  executeUpdateDaily,
	}

	TaskInitDaily = &Task{
		Name:      "init_daily",
		DependsOn: []string{},
		Executor:  executeInitDaily,
	}

	TaskUpdateGBBQ = &Task{
		Name:      "update_gbbq",
		DependsOn: []string{},
		SkipIf:    skipIfPlan(func(p *WorkPlan) bool { return !p.NeedGbbq }),
		Executor:  executeUpdateGBBQ,
	}

	TaskCalcBasic = &Task{
		Name:      "calc_basic",
		DependsOn: []string{"update_daily", "update_gbbq"},
		SkipIf:    skipIfPlan(func(p *WorkPlan) bool { return !p.NeedBasic }),
		Executor:  executeCalcBasic,
	}

	TaskCalcFactor = &Task{
		Name:      "calc_factor",
		DependsOn: []string{"calc_basic"},
		SkipIf:    skipIfPlan(func(p *WorkPlan) bool { return !p.NeedFactor }),
		Executor:  executeCalcFactor,
	}

	TaskUpdate1Min = &Task{
		Name:      "update_1min",
		DependsOn: []string{},
		SkipIf: func(ctx context.Context, db database.DataRepository, args *TaskArgs) bool {
			return !args.Min
		},
		Executor: executeUpdate1Min,
		OnError:  ErrorModeSkip,
	}

	TaskUpdateHolidays = &Task{
		Name:      "update_holidays",
		DependsOn: []string{"update_gbbq"},
		SkipIf:    skipIfPlan(func(p *WorkPlan) bool { return !p.NeedHolidays }),
		Executor:  executeUpdateHolidays,
		OnError:   ErrorModeSkip,
	}
}

// skipIfPlan 仅在 Plan 存在且谓词判为 true 时跳过；Plan 为 nil（如 init 流程）时保持原行为。
func skipIfPlan(predicate func(*WorkPlan) bool) SkipCondition {
	return func(ctx context.Context, db database.DataRepository, args *TaskArgs) bool {
		if args.Plan == nil {
			return false
		}
		return predicate(args.Plan)
	}
}

func executeUpdateDaily(ctx context.Context, db database.DataRepository, args *TaskArgs) (*TaskResult, error) {

	latestDate, err := db.GetLatestDate(model.TableKlineDaily.TableName, "date")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest date from database: %w", err)
	}
	fmt.Printf("📅 日线数据最新日期为 %s\n", latestDate.Format("2006-01-02"))

	validDates, err := prepareTdxData(ctx, latestDate, "day", args)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare tdx data: %w", err)
	}

	if len(validDates) == 0 {
		fmt.Println("🌲 日线数据无需更新")
		return &TaskResult{State: StateSkipped, Message: "no new daily data"}, nil
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

	if err := db.ImportKlineDaily(stockDailyCSV); err != nil {
		return nil, fmt.Errorf("failed to import stock csv: %w", err)
	}

	if err := db.RebuildSymbolClass(); err != nil {
		return nil, fmt.Errorf("failed to rebuild symbol_class: %w", err)
	}

	fmt.Println("🚀 股票数据导入成功")
	return &TaskResult{State: StateCompleted, Message: "daily data imported"}, nil
}

func executeUpdateGBBQ(ctx context.Context, db database.DataRepository, args *TaskArgs) (*TaskResult, error) {
	fmt.Println("🐢 开始下载股本变迁数据")

	gbbqFile, err := getGbbqFile(args.TempDir)
	if err != nil {
		return nil, fmt.Errorf("failed to download GBBQ file: %w", err)
	}

	gbbqData, err := tdx.DecodeGbbqFile(gbbqFile)
	if err != nil {
		return nil, fmt.Errorf("failed to decode GBBQ: %w", err)
	}

	gbbqCSV := filepath.Join(args.TempDir, "gbbq.csv")
	gbbqCw, err := utils.NewCSVWriter[model.GbbqData](gbbqCSV)
	if err != nil {
		return nil, fmt.Errorf("failed to create GBBQ CSV writer: %w", err)
	}
	if err := gbbqCw.Write(gbbqData); err != nil {
		return nil, err
	}
	gbbqCw.Close()

	if err := db.ImportGBBQ(gbbqCSV); err != nil {
		return nil, fmt.Errorf("failed to import GBBQ csv into database: %w", err)
	}

	fmt.Println("📈 股本变迁数据导入成功")
	return &TaskResult{State: StateCompleted, Rows: len(gbbqData), Message: "gbbq data imported"}, nil
}

func executeCalcBasic(ctx context.Context, db database.DataRepository, args *TaskArgs) (*TaskResult, error) {
	fmt.Println("📟 计算股票基础行情")
	basicCSV := filepath.Join(args.TempDir, "basics.csv")

	rowCount, err := calc.ExportBasicDailyToCSV(ctx, db, basicCSV)
	if err != nil {
		return nil, fmt.Errorf("failed to export basic to csv: %w", err)
	}

	if rowCount == 0 {
		fmt.Println("🌲 股票基础行情无需更新")
		return &TaskResult{State: StateSkipped, Message: "no new basic data"}, nil
	}

	db.TruncateTable(model.TableBasicDaily)
	if err := db.ImportBasic(basicCSV); err != nil {
		return nil, fmt.Errorf("failed to import basic data: %w", err)
	}
	fmt.Println("🔢 基础行情导入成功")
	return &TaskResult{State: StateCompleted, Rows: rowCount, Message: "basic data calculated"}, nil
}

func executeCalcFactor(ctx context.Context, db database.DataRepository, args *TaskArgs) (*TaskResult, error) {
	fmt.Println("📟 计算股票复权因子")
	factorCSV := filepath.Join(args.TempDir, "factor.csv")

	factorCount, err := calc.ExportFactorsToCSV(ctx, db, factorCSV)
	if err != nil {
		return nil, fmt.Errorf("failed to export factor to csv: %w", err)
	}

	if factorCount == 0 {
		fmt.Println("🌲 复权因子无需更新")
		return &TaskResult{State: StateSkipped, Message: "no new factor data"}, nil
	}

	db.TruncateTable(model.TableAdjustFactor)
	if err := db.ImportAdjustFactors(factorCSV); err != nil {
		return nil, fmt.Errorf("failed to append factor data: %w", err)
	}
	fmt.Printf("🔢 复权因子导入成功\n")
	return &TaskResult{State: StateCompleted, Rows: factorCount, Message: "factors calculated"}, nil
}

func executeUpdate1Min(ctx context.Context, db database.DataRepository, args *TaskArgs) (*TaskResult, error) {
	latestDate, err := getMin1LatestDate(db, args)
	if err != nil {
		return nil, err
	}

	validDates, err := prepareTdxData(ctx, latestDate, "tic", args)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare tdx data: %w", err)
	}

	if len(validDates) >= 30 {
		return nil, fmt.Errorf("分时数据超过30天未更新，请手动补齐后继续")
	}

	if len(validDates) > 0 {
		fmt.Printf("🐌 开始转换1分钟分时数据 (共 %d 天)\n", len(validDates))

		stock1MinCSV := filepath.Join(args.TempDir, "1min.csv")

		// 路径策略：优先从 TempDir 找（那是 datatool 刚刚转码产出的地方）
		inputDir := args.VipdocDir
		if _, err := os.Stat(filepath.Join(args.TempDir, "vipdoc")); err == nil {
			inputDir = args.TempDir
			fmt.Printf("📦 检测到新转档数据，正在从临时目录加载...\n")
		}

		_, err := tdx.ConvertFilesToCSV(ctx, inputDir, stock1MinCSV, ".01")
		if err != nil {
			return nil, fmt.Errorf("failed to convert .01 files to csv: %w", err)
		}

		if err := db.ImportKline1Min(stock1MinCSV); err != nil {
			return nil, fmt.Errorf("failed to import 1-minute line csv: %w", err)
		}
		fmt.Println("📊 1分钟数据导入成功")
		return &TaskResult{State: StateCompleted, Message: "1min data imported"}, nil
	}

	fmt.Println("🌲 1分钟分时数据无需更新")
	return &TaskResult{State: StateSkipped, Message: "no new 1min data"}, nil
}

func executeUpdateHolidays(ctx context.Context, db database.DataRepository, args *TaskArgs) (*TaskResult, error) {
	fmt.Printf("🐢 导入通达信交易日历\n")
	zhbZipPath := filepath.Join(args.TempDir, "gbbq-temp", "zhb.zip")
	holidaysFile, err := tdx.ExportTdxHolidaysToCSV(zhbZipPath, args.TempDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "🚨 警告: %v\n", err)
		return &TaskResult{State: StateFailed, Error: err, Message: "holidays import warning"}, nil
	}

	if err := db.ImportHolidays(holidaysFile); err != nil {
		return nil, fmt.Errorf("failed to import holidays: %w", err)
	}

	return &TaskResult{State: StateCompleted, Message: "holidays data imported"}, nil
}

func getMin1LatestDate(db database.DataRepository, args *TaskArgs) (time.Time, error) {
	latestDate, err := db.GetLatestDate(model.TableKline1Min.TableName, "datetime")
	if err != nil {
		return time.Time{}, err
	}

	if latestDate.IsZero() {
		fmt.Println("🛑 警告：数据库中没有 1分钟 数据")
		fmt.Println("🚧 将处理今天的数据，历史请自行导入")
		return args.Today.AddDate(0, 0, -1), nil
	}

	return latestDate, nil
}

func prepareTdxData(ctx context.Context, latestDate time.Time, dataType string, args *TaskArgs) ([]time.Time, error) {
	var dates []time.Time
	// 优先使用 TargetDate (补数据模式)
	if args.TargetDate != "" {
		t, err := time.Parse("20060102", args.TargetDate)
		if err == nil {
			dates = []time.Time{t}
		}
	}

	// 如果没有 TargetDate，则按增量逻辑处理
	if len(dates) == 0 {
		for d := latestDate.Add(24 * time.Hour); !d.After(args.Today); d = d.Add(24 * time.Hour) {
			dates = append(dates, d)
		}
	}

	if len(dates) == 0 {
		return nil, nil
	}

	homeDir, _ := os.UserHomeDir()
	baseDownloadDir := filepath.Join(homeDir, "Downloads", "tdx_data", dataType)
	
	urlTemplate := ""
	dataTypeCN := ""
	switch dataType {
	case "day":
		urlTemplate = "https://www.tdx.com.cn/products/data/data/g4day/%s.zip"
		dataTypeCN = "日线数据"
	case "tic":
		urlTemplate = "https://www.tdx.com.cn/products/data/data/g4tic/%s.zip"
		dataTypeCN = "分时数据"
	default:
		return nil, fmt.Errorf("unknown data type: %s", dataType)
	}

	if err := os.MkdirAll(baseDownloadDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create download directory: %w", err)
	}

	// 强制清空旧的链接目录，防止旧数据干扰
	os.RemoveAll(filepath.Join(args.VipdocDir, "refmhq"))
	os.RemoveAll(filepath.Join(args.VipdocDir, "newdatetick"))

	fmt.Printf("🐌 开始准备%s (存储目录: %s)\n", dataTypeCN, baseDownloadDir)

	validDates := make([]time.Time, 0, len(dates))

	for _, date := range dates {
		select {
		case <-ctx.Done():
			return validDates, ctx.Err()
		default:
		}

		// 节假日判定
		if args.Plan != nil && args.Plan.Calendar != nil {
			if args.Plan.Calendar.IsHoliday(date) || args.Plan.Calendar.IsWeekend(date) {
				continue
			}
		}

		dateStr := date.Format("20060102")
		url := fmt.Sprintf(urlTemplate, dateStr)
		zipPath := filepath.Join(baseDownloadDir, dateStr+".zip")
		extractDir := filepath.Join(baseDownloadDir, dateStr)

		// 1. 下载 (带续传)
		if _, err := os.Stat(zipPath); os.IsNotExist(err) {
			fmt.Printf("📥 正在下载 %s %s...\n", dataTypeCN, dateStr)
			if err := utils.DownloadFileWithResume(url, zipPath); err != nil {
				fmt.Printf("⚠️  下载跳过 %s: %v (可能尚未发布)\n", dateStr, err)
				continue
			}
		}

		// 2. 解压 (如果解压目录不存在)
		if _, err := os.Stat(extractDir); os.IsNotExist(err) {
			fmt.Printf("🔓 正在解压 %s...\n", dateStr)
			if err := utils.UnzipFile(zipPath, extractDir); err != nil {
				fmt.Printf("⚠️  解压失败 %s: %v\n", dateStr, err)
				continue
			}
		}

		// 3. 链接到工作目录，供 datatool 使用
		var tdxSubDir string
		if dataType == "day" {
			tdxSubDir = filepath.Join(args.VipdocDir, "refmhq")
		} else {
			tdxSubDir = filepath.Join(args.VipdocDir, "newdatetick")
		}
		os.MkdirAll(tdxSubDir, 0755)

		// 遍历解压目录，将文件链接/复制到 tdxSubDir
		files, _ := os.ReadDir(extractDir)
		for _, f := range files {
			src := filepath.Join(extractDir, f.Name())
			dst := filepath.Join(tdxSubDir, f.Name())
			_ = os.Remove(dst) // 清理旧链接
			_ = os.Symlink(src, dst)
		}

		validDates = append(validDates, date)
	}

	if len(validDates) > 0 {
		endDate := validDates[len(validDates)-1]
		switch dataType {
		case "day":
			if err := tdx.DatatoolCreate(args.TempDir, args.VipdocDir, "day", endDate); err != nil {
				return nil, fmt.Errorf("failed to run DatatoolDayCreate: %w", err)
			}
		case "tic":
			fmt.Printf("🐌 开始转档分笔数据\n")
			if err := tdx.DatatoolCreate(args.TempDir, args.VipdocDir, "tick", endDate); err != nil {
				return nil, fmt.Errorf("failed to run DatatoolTickCreate: %w", err)
			}
			fmt.Printf("🐌 开始转换分钟数据\n")
			if err := tdx.DatatoolCreate(args.TempDir, args.VipdocDir, "min", endDate); err != nil {
				return nil, fmt.Errorf("failed to run DatatoolMinCreate: %w", err)
			}
		}

		// 核心清理：任务完成后，删掉解压出的日期文件夹，只保留 Zip
		for _, date := range validDates {
			dateStr := date.Format("20060102")
			extractDir := filepath.Join(baseDownloadDir, dateStr)
			os.RemoveAll(extractDir)
		}
	}

	return validDates, nil
}

func getGbbqFile(cacheDir string) (string, error) {
	zipPath := filepath.Join(cacheDir, "gbbq.zip")
	gbbqURL := "http://www.tdx.com.cn/products/data/data/dbf/gbbq.zip"
	if err := utils.DownloadFileWithResume(gbbqURL, zipPath); err != nil {
		return "", fmt.Errorf("failed to download GBBQ zip file: %w", err)
	}

	unzipPath := filepath.Join(cacheDir, "gbbq-temp")
	if err := utils.UnzipFile(zipPath, unzipPath, true); err != nil {
		return "", fmt.Errorf("failed to unzip GBBQ file: %w", err)
	}

	return filepath.Join(unzipPath, "gbbq"), nil
}

func GetUpdateTaskNames() []string {
	return []string{
		"update_daily",
		"update_gbbq",
		"calc_basic",
		"calc_factor",
		"update_1min",
		"update_holidays",
	}
}

func GetRegisteredTasks() map[string]*Task {
	return map[string]*Task{
		"update_daily":    TaskUpdateDaily,
		"init_daily":      TaskInitDaily,
		"update_gbbq":     TaskUpdateGBBQ,
		"calc_basic":      TaskCalcBasic,
		"calc_factor":     TaskCalcFactor,
		"update_1min":     TaskUpdate1Min,
		"update_holidays": TaskUpdateHolidays,
	}
}

func GetInitTaskNames() []string {
	return []string{
		"init_daily",
	}
}
