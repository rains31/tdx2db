package tdx

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/utils"
)

const (
	recordSize = 32
)

// cnLoc 是中国标准时间 (Asia/Shanghai, UTC+8)。
// 通达信数据文件中的时间字段不含时区信息，且所有 A 股行情均为北京时间，
// 因此解析时必须显式使用 CST 而非 time.Local，避免在 UTC 服务器上产生 8 小时偏移。
var cnLoc = time.FixedZone("CST", 8*3600)

// ConvertFilesToCSV 转换 TDX 文件到 CSV
func ConvertFilesToCSV(ctx context.Context, inputDir string, outputFile string, suffix string) (string, error) {
	switch suffix {
	case ".day":
		return runConversion[model.KlineDay](ctx, inputDir, outputFile, suffix, processDayFile)
	case ".01":
		return runConversion[model.KlineMin](ctx, inputDir, outputFile, suffix, processMinFile)
	default:
		return "", fmt.Errorf("unsupported suffix: %s", suffix)
	}
}

func runConversion[T any](
	ctx context.Context,
	inputDir string,
	outputFile string,
	suffix string,
	parser func([]byte, string) ([]T, error),
) (string, error) {

	files, err := collectFiles(inputDir, suffix)
	if err != nil {
		return "", err
	}

	if len(files) == 0 {
		return outputFile, nil
	}

	cw, err := utils.NewCSVWriter[T](outputFile)
	if err != nil {
		return "", fmt.Errorf("failed to create CSV writer: %w", err)
	}
	defer cw.Close()

	pipeline := utils.NewPipeline[string, T]()

	result, err := pipeline.Run(
		ctx,
		files,
		func(ctx context.Context, file string) ([]T, error) {
			return readFileAndParse(ctx, file, suffix, parser)
		},
		func(rows []T) error {
			return cw.Write(rows)
		},
	)

	if err != nil {
		return outputFile, err
	}

	if result.HasErrors() {
		return outputFile, fmt.Errorf("occurred %d errors, first: %v",
			len(result.Errors), result.FirstError())
	}

	return outputFile, nil
}

func readFileAndParse[T any](
	ctx context.Context,
	filename string,
	suffix string,
	parser func([]byte, string) ([]T, error),
) ([]T, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filename, err)
	}

	if len(data) == 0 {
		return nil, nil
	}

	symbol := strings.TrimSuffix(filepath.Base(filename), suffix)
	return parser(data, symbol)
}

func processDayFile(data []byte, symbol string) ([]model.KlineDay, error) {
	n := len(data)
	if n%recordSize != 0 {
		return nil, fmt.Errorf("invalid file size: %d", n)
	}
	count := n / recordSize
	rows := make([]model.KlineDay, 0, count)
	scale := model.PriceScale(symbol)
	indexMode := isIndexMode(symbol)

	var offset int
	for i := 0; i < count; i++ {
		offset = i * recordSize

		dateRaw := binary.LittleEndian.Uint32(data[offset : offset+4])
		openRaw := binary.LittleEndian.Uint32(data[offset+4 : offset+8])
		highRaw := binary.LittleEndian.Uint32(data[offset+8 : offset+12])
		lowRaw := binary.LittleEndian.Uint32(data[offset+12 : offset+16])
		closeRaw := binary.LittleEndian.Uint32(data[offset+16 : offset+20])

		amountBits := binary.LittleEndian.Uint32(data[offset+20 : offset+24])
		amount := math.Float32frombits(amountBits)

		volRaw := binary.LittleEndian.Uint32(data[offset+24 : offset+28])
		reserved := binary.LittleEndian.Uint32(data[offset+28 : offset+32])
		volume := int64(volRaw)
		var upCount, downCount int64
		if indexMode {
			upCount, downCount = parseDayBreadth(reserved)
		} else {
			volume = parseDayVolume(volRaw, reserved)
		}

		t, err := parseDate(dateRaw)
		if err != nil {
			continue
		}

		rows = append(rows, model.KlineDay{
			Symbol:    symbol,
			Open:      float64(openRaw) / scale,
			High:      float64(highRaw) / scale,
			Low:       float64(lowRaw) / scale,
			Close:     float64(closeRaw) / scale,
			Amount:    float64(amount),
			Volume:    volume,
			UpCount:   upCount,
			DownCount: downCount,
			Date:      t,
		})
	}
	return rows, nil
}

func processMinFile(data []byte, symbol string) ([]model.KlineMin, error) {
	n := len(data)
	if n%recordSize != 0 {
		return nil, fmt.Errorf("invalid file size: %d", n)
	}
	count := n / recordSize
	rows := make([]model.KlineMin, 0, count)
	scale := model.PriceScale(symbol)

	var offset int
	for i := 0; i < count; i++ {
		offset = i * recordSize

		dateRaw := binary.LittleEndian.Uint16(data[offset : offset+2])
		timeRaw := binary.LittleEndian.Uint16(data[offset+2 : offset+4])
		openRaw := binary.LittleEndian.Uint32(data[offset+4 : offset+8])
		highRaw := binary.LittleEndian.Uint32(data[offset+8 : offset+12])
		lowRaw := binary.LittleEndian.Uint32(data[offset+12 : offset+16])
		closeRaw := binary.LittleEndian.Uint32(data[offset+16 : offset+20])

		amountBits := binary.LittleEndian.Uint32(data[offset+20 : offset+24])
		amount := math.Float32frombits(amountBits)

		volRaw := binary.LittleEndian.Uint32(data[offset+24 : offset+28])

		t, err := parseDateTime(dateRaw, timeRaw)
		if err != nil {
			continue
		}

		rows = append(rows, model.KlineMin{
			Symbol:   symbol,
			Open:     float64(openRaw) / scale,
			High:     float64(highRaw) / scale,
			Low:      float64(lowRaw) / scale,
			Close:    float64(closeRaw) / scale,
			Amount:   float64(amount),
			Volume:   int64(volRaw),
			Datetime: t,
		})
	}
	return rows, nil
}

func parseDate(d uint32) (time.Time, error) {
	year := int(d / 10000)
	month := int((d % 10000) / 100)
	day := int(d % 100)
	if year < 1900 || month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, fmt.Errorf("invalid date")
	}
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, cnLoc), nil
}

func parseDayVolume(volRaw, reserved uint32) int64 {
	if reserved&0xffffff00 == 0xc3640000 {
		return int64(volRaw)*100 + int64(reserved&0xff)
	}
	return int64(volRaw)
}

func parseDayBreadth(reserved uint32) (upCount, downCount int64) {
	return int64(reserved & 0xffff), int64(reserved >> 16)
}

func isIndexMode(symbol string) bool {
	class := model.ClassifyCode(symbol)
	return class == model.ClassIndex || class == model.ClassBlock
}

func parseDateTime(dateRaw, timeRaw uint16) (time.Time, error) {
	year := int(dateRaw)/2048 + 2004
	month := (int(dateRaw) % 2048) / 100
	day := (int(dateRaw) % 2048) % 100
	hour := int(timeRaw) / 60
	minute := int(timeRaw) % 60

	// datatool 闭源二进制在某些调用路径下会将 CST 交易时间 (9-15)
	// 写入为 +8h 偏移 (17-23)。仅在明显超出交易时段时才回拨。
	if hour >= 16 {
		hour -= 8
	}

	if month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, fmt.Errorf("invalid date")
	}
	return time.Date(year, time.Month(month), day, hour, minute, 0, 0, cnLoc), nil
}

var symbolPattern = regexp.MustCompile(`^(sh|sz|bj)\d+$`)

func collectFiles(root string, suffix string) ([]string, error) {
	var files []string

	var walk func(string) error
	walk = func(path string) error {
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			fullPath := filepath.Join(path, entry.Name())
			info, err := entry.Info()
			if err != nil {
				continue
			}

			// 如果是软链接，获取其指向的真实信息
			if info.Mode()&os.ModeSymlink != 0 {
				realPath, err := os.Readlink(fullPath)
				if err == nil {
					if !filepath.IsAbs(realPath) {
						realPath = filepath.Join(path, realPath)
					}
					info, err = os.Stat(realPath)
					if err == nil && info.IsDir() {
						if err := walk(fullPath); err != nil {
							return err
						}
						continue
					}
				}
			}
			if info == nil {
				continue
			}
			if info.IsDir() {
				if err := walk(fullPath); err != nil {
					return err
				}
			} else if strings.HasSuffix(fullPath, suffix) {
				symbol := strings.TrimSuffix(entry.Name(), suffix)
				if symbolPattern.MatchString(symbol) && model.IsCoreSecurity(symbol) {
					files = append(files, fullPath)
				}
			}
		}
		return nil
	}

	err := walk(root)
	return files, err
}

