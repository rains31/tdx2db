package calc

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/utils"
)

// FactorContext 处理上下文
type FactorContext struct {
	DB          database.DataRepository
	GbbqIndex   GbbqIndex
	ExistingMap map[string]map[string]float64
	HolidaysMap map[string]bool
	Force       bool
}

// ExportFactorsToCSV 导出后复权因子（每天一条记录，增量补充，且向前补充 5 个未来交易日的因子）
func ExportFactorsToCSV(
	ctx context.Context,
	db database.DataRepository,
	csvPath string,
	force bool,
) (int, error) {
	gbbqData, err := db.GetGbbq()
	if err != nil {
		return 0, fmt.Errorf("failed to query gbbq: %w", err)
	}
	gbbqIndex := buildGbbqIndex(gbbqData)

	holidays, err := db.GetHolidays()
	if err != nil {
		return 0, fmt.Errorf("failed to query holidays: %w", err)
	}
	holidaysMap := make(map[string]bool)
	for _, h := range holidays {
		holidaysMap[h.Format("2006-01-02")] = true
	}

	symbols, err := db.GetSymbolsByClass(model.ClassStock, model.ClassETF)
	if err != nil {
		return 0, fmt.Errorf("failed to query symbols: %w", err)
	}

	if len(symbols) == 0 {
		return 0, nil
	}

	// 如果不是 force 模式，预加载现有的复权因子
	existingMap := make(map[string]map[string]float64)
	if !force {
		var existingFactors []model.Factor
		err = db.Query(model.TableAdjustFactor.TableName, nil, &existingFactors)
		if err == nil {
			for _, f := range existingFactors {
				sym := f.Symbol
				dateStr := f.Date.Format("2006-01-02")
				if _, ok := existingMap[sym]; !ok {
					existingMap[sym] = make(map[string]float64)
				}
				existingMap[sym][dateStr] = f.HfqFactor
			}
		}
	}

	cw, err := utils.NewCSVWriter[model.Factor](csvPath)
	if err != nil {
		return 0, err
	}
	defer cw.Close()

	fctx := &FactorContext{
		DB:          db,
		GbbqIndex:   gbbqIndex,
		ExistingMap: existingMap,
		HolidaysMap: holidaysMap,
		Force:       force,
	}

	var mu sync.Mutex
	updatedSymbols := make(map[string]bool)

	pipeline := utils.NewPipeline[string, model.Factor]()

	result, err := pipeline.Run(
		ctx,
		symbols,
		func(ctx context.Context, symbol string) ([]model.Factor, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			return processFactorSymbol(fctx, symbol)
		},
		func(rows []model.Factor) error {
			if len(rows) > 0 {
				mu.Lock()
				updatedSymbols[rows[0].Symbol] = true
				mu.Unlock()
			}
			return cw.Write(rows)
		},
	)

	if err != nil {
		return 0, err
	}

	if result.HasErrors() {
		return 0, fmt.Errorf("export completed with %s", result.ErrorSummary())
	}

	// 增量模式下，清理这些有更新的股票在 ClickHouse 中的旧复权数据，再在外部 Append 进去
	if !force && len(updatedSymbols) > 0 {
		symbolsList := make([]string, 0, len(updatedSymbols))
		for sym := range updatedSymbols {
			symbolsList = append(symbolsList, sym)
		}

		batchSize := 100
		for i := 0; i < len(symbolsList); i += batchSize {
			end := i + batchSize
			if end > len(symbolsList) {
				end = len(symbolsList)
			}
			batch := symbolsList[i:end]

			symbolsStr := make([]string, len(batch))
			for idx, sym := range batch {
				symbolsStr[idx] = fmt.Sprintf("'%s'", sym)
			}
			whereClause := fmt.Sprintf("symbol IN (%s)", strings.Join(symbolsStr, ","))
			err := db.DeleteWhere(model.TableAdjustFactor.TableName, whereClause)
			if err != nil {
				return 0, fmt.Errorf("failed to delete old factors: %w", err)
			}
		}
		fmt.Printf("🗑️  已清理 %d 个变更股票的原复权因子记录\n", len(symbolsList))
	}

	return int(result.OutputRows), nil
}

func processFactorSymbol(fctx *FactorContext, symbol string) ([]model.Factor, error) {
	basics, err := fctx.DB.GetBasicsBySymbol(symbol)
	if err != nil {
		return nil, fmt.Errorf("query %s failed: %w", symbol, err)
	}
	if len(basics) == 0 {
		return nil, nil
	}

	gbbqs := getGbbqBySymbol(fctx.GbbqIndex, symbol)
	lastDate := basics[len(basics)-1].Date

	// 找出从最新交易日开始的接下来 5 个未来交易日（排除周末和 raw_holidays 法定节假日）
	var futureDates []time.Time
	curr := lastDate
	for len(futureDates) < 5 {
		curr = curr.AddDate(0, 0, 1)
		// 排除周末
		if curr.Weekday() == time.Saturday || curr.Weekday() == time.Sunday {
			continue
		}
		// 排除法定节假日
		if fctx.HolidaysMap[curr.Format("2006-01-02")] {
			continue
		}
		futureDates = append(futureDates, curr)
	}

	// 将 GBBQ 按日期索引，用于快速查找特定日期的变更
	gbbqsByDate := make(map[string][]model.GbbqData)
	for _, item := range gbbqs {
		if item.Category == 1 || (item.Category == 11 && item.C3 > 0) {
			dStr := item.Date.Format("2006-01-02")
			gbbqsByDate[dStr] = append(gbbqsByDate[dStr], item)
		}
	}

	// 顺序构建未来 5 个交易日的 dummy basic 记录
	prevClose := basics[len(basics)-1].Close
	for _, fd := range futureDates {
		dStr := fd.Format("2006-01-02")
		items := gbbqsByDate[dStr]

		xdxr := &xdxrInfo{}
		for _, item := range items {
			if item.Category == 1 {
				xdxr.Fenhong += item.C1
				xdxr.Songzhuangu += item.C3
				xdxr.Peigu += item.C4
				if item.C2 > 0 {
					xdxr.Peigujia = item.C2
				}
			} else if item.Category == 11 && item.C3 > 0 {
				if xdxr.Splitc3 > 0 {
					xdxr.Splitc3 *= item.C3
				} else {
					xdxr.Splitc3 = item.C3
				}
			}
		}

		preClose := calculatePreClosePrice(prevClose, xdxr)

		dummy := model.BasicDaily{
			Symbol:   symbol,
			Date:     fd,
			Close:    prevClose,
			PreClose: preClose,
		}
		basics = append(basics, dummy)
		prevClose = dummy.Close
	}

	// 计算所有交易日（包括补充的 5 个未来交易日）的后复权因子
	allFactors := calculateFullHfq(basics)

	// 对比已有缓存，实现完全匹配时增量跳过
	if !fctx.Force {
		existing, ok := fctx.ExistingMap[symbol]
		if ok && len(existing) == len(allFactors) {
			matchAll := true
			for _, f := range allFactors {
				dStr := f.Date.Format("2006-01-02")
				val, exists := existing[dStr]
				if !exists || math.Abs(val-f.HfqFactor) > 1e-7 {
					matchAll = false
					break
				}
			}
			if matchAll {
				return nil, nil // 完全没有变化，静默跳过
			}
		}
	}

	return allFactors, nil
}

// calculateFullHfq 全量计算 HFQ（每天一条记录）
// basic.PreClose 已包含除权调整，prevClose != PreClose 即为除权日，无需独立的 xdxr 日期集合
func calculateFullHfq(basics []model.BasicDaily) []model.Factor {
	if len(basics) == 0 {
		return nil
	}

	results := make([]model.Factor, 0, len(basics))
	currentHfq := 1.0
	prevClose := basics[0].Close

	// 第一天
	results = append(results, model.Factor{
		Symbol:    basics[0].Symbol,
		Date:      basics[0].Date,
		HfqFactor: currentHfq,
	})

	// 后续每天：通过 prevClose / PreClose 检测除权
	for i := 1; i < len(basics); i++ {
		basic := basics[i]
		if basic.PreClose != 0 {
			ratio := prevClose / basic.PreClose
			if !floatEqual(ratio, 1.0) {
				currentHfq *= ratio
			}
		}
		results = append(results, model.Factor{
			Symbol:    basic.Symbol,
			Date:      basic.Date,
			HfqFactor: currentHfq,
		})
		prevClose = basic.Close
	}

	return results
}

func floatEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}
