// 本文件汇集 TDX 相关任务共用的工具: 按日期范围下载并解压 zip 的通用流程。
package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jing2uo/tdx2db/utils"
)

// pullSource 描述一组按日期下载的 TDX zip 源。
type pullSource struct {
	targetDir   string // 解压目标目录（绝对路径）
	urlTemplate string // 形如 "https://.../%s.zip"
	fileSuffix  string // 文件名末尾标识，例如 "day" / "tic"
	label       string // 日志中文标签，例如 "日线" / "分时"
}

// pullDateRange 从 since 之后到 today 之间逐日下载 zip 并解压。
// 404 状态会结合 args.Plan.Calendar 区分"节假日跳过"/"数据尚未发布"。
// 返回实际成功下载（200）的日期列表，调用方据此决定后续转档/导入。
func pullDateRange(ctx context.Context, since time.Time, src pullSource, args *TaskArgs) ([]time.Time, error) {
	var dates []time.Time
	if args.TargetDate != "" {
		t, err := time.Parse("20060102", args.TargetDate)
		if err == nil {
			dates = []time.Time{t}
		}
	} else {
		for d := since.Add(24 * time.Hour); !d.After(args.Today); d = d.Add(24 * time.Hour) {
			dates = append(dates, d)
		}
	}
	if len(dates) == 0 {
		return nil, nil
	}

	homeDir, _ := os.UserHomeDir()
	baseDownloadDir := filepath.Join(homeDir, "Downloads", "tdx_data", src.fileSuffix)

	if err := os.MkdirAll(baseDownloadDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create download directory: %w", err)
	}

	// 强制清空旧的链接目录，防止旧数据干扰
	os.RemoveAll(src.targetDir)
	if err := os.MkdirAll(src.targetDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create target directory: %w", err)
	}

	fmt.Printf("🐌 开始准备%s数据 (存储目录: %s)\n", src.label, baseDownloadDir)

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
		url := fmt.Sprintf(src.urlTemplate, dateStr)
		zipPath := filepath.Join(baseDownloadDir, dateStr+".zip")
		extractDir := filepath.Join(baseDownloadDir, dateStr)

		// 1. 下载 (带续传)
		if _, err := os.Stat(zipPath); os.IsNotExist(err) {
			fmt.Printf("📥 正在下载 %s %s...\n", src.label, dateStr)
			if err := utils.DownloadFileWithResume(url, zipPath); err != nil {
				var cal *TradingCalendar
				if args.Plan != nil {
					cal = args.Plan.Calendar
				}
				switch {
				case cal != nil && cal.IsHoliday(date):
					fmt.Printf("🎉 %s 为节假日，跳过\n", dateStr)
				case cal != nil && cal.IsWeekend(date):
					fmt.Printf("🌴 %s 为周末，跳过\n", dateStr)
				case date.Equal(args.Today):
					fmt.Printf("⏳ %s 数据尚未发布，请等待收盘后重试\n", dateStr)
				default:
					fmt.Printf("🟡 %s 数据尚未发布: %v\n", dateStr, err)
				}
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

		// 3. 复制到工作目录，供 datatool 使用
		files, _ := os.ReadDir(extractDir)
		for _, f := range files {
			srcFile := filepath.Join(extractDir, f.Name())
			dstFile := filepath.Join(src.targetDir, f.Name())
			_ = os.Remove(dstFile) // 清理旧链接/文件
			
			input, err := os.ReadFile(srcFile)
			if err == nil {
				_ = os.WriteFile(dstFile, input, 0644)
			}
		}

		validDates = append(validDates, date)
	}

	// 核心清理：任务完成后，删掉解压出的日期文件夹，只保留 Zip
	if len(validDates) > 0 {
		for _, date := range validDates {
			dateStr := date.Format("20060102")
			extractDir := filepath.Join(baseDownloadDir, dateStr)
			os.RemoveAll(extractDir)
		}
	}

	return validDates, nil
}
