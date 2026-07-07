package calc

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/utils"
)

// FactorContext 处理上下文
type FactorContext struct {
	DB        database.DataRepository
	GbbqIndex GbbqIndex
}

// ExportFactorsToCSV 导出后复权因子（每天一条记录，和日线对齐）
func ExportFactorsToCSV(
	ctx context.Context,
	db database.DataRepository,
	csvPath string,
) (int, error) {
	gbbqData, err := db.GetGbbq()
	if err != nil {
		return 0, fmt.Errorf("failed to query gbbq: %w", err)
	}
	gbbqIndex := buildGbbqIndex(gbbqData)

	symbols, err := db.GetSymbolsByClass(model.ClassStock, model.ClassETF)
	if err != nil {
		return 0, fmt.Errorf("failed to query symbols: %w", err)
	}

	if len(symbols) == 0 {
		return 0, nil
	}

	cw, err := utils.NewCSVWriter[model.Factor](csvPath)
	if err != nil {
		return 0, err
	}
	defer cw.Close()

	fctx := &FactorContext{
		DB:        db,
		GbbqIndex: gbbqIndex,
	}

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
			return cw.Write(rows)
		},
	)

	if err != nil {
		return 0, err
	}

	if result.HasErrors() {
		return 0, fmt.Errorf("export completed with %s", result.ErrorSummary())
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

	// 找出未来可能影响复权因子的 GBBQ 事件 (Category 1 和 11)
	var futureEvents []model.GbbqData
	for _, item := range gbbqs {
		if item.Date.After(lastDate) && (item.Category == 1 || (item.Category == 11 && item.C3 > 0)) {
			futureEvents = append(futureEvents, item)
		}
	}

	if len(futureEvents) > 0 {
		eventsByDate := make(map[string][]model.GbbqData)
		var futureDates []time.Time
		for _, item := range futureEvents {
			dStr := item.Date.Format("2006-01-02")
			if _, ok := eventsByDate[dStr]; !ok {
				futureDates = append(futureDates, item.Date)
			}
			eventsByDate[dStr] = append(eventsByDate[dStr], item)
		}

		sort.Slice(futureDates, func(i, j int) bool {
			return futureDates[i].Before(futureDates[j])
		})

		prevClose := basics[len(basics)-1].Close
		for _, fd := range futureDates {
			dStr := fd.Format("2006-01-02")
			items := eventsByDate[dStr]

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
	}

	return calculateFullHfq(basics), nil
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
