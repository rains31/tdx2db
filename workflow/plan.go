package workflow

import (
	"fmt"
	"time"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
)

// WorkPlan 在任务图启动前汇总交易日历与各表最新日期，决定哪些任务真正需要跑。
type WorkPlan struct {
	Today          time.Time
	LastTradingDay time.Time
	Calendar       *TradingCalendar

	NeedDaily    bool
	NeedGbbq     bool
	NeedBasic    bool
	NeedFactor   bool
	NeedHolidays bool
	NeedMin      bool

	IsTradingTime bool
	Reason        string // 用于日志
}

// AnyNeeded 是否有任何任务需要执行。
func (p *WorkPlan) AnyNeeded() bool {
	return p.NeedDaily || p.NeedGbbq || p.NeedBasic || p.NeedFactor || p.NeedHolidays || p.NeedMin
}

// BuildWorkPlan 读取交易日历与各表最新日期，推导本次 cron 要做什么。
func BuildWorkPlan(db database.DataRepository, today time.Time, day bool, min bool, targetDate string, missingOnly bool, force bool, gbbq bool) (*WorkPlan, error) {
	holidays, err := db.GetHolidays()
	if err != nil {
		return nil, fmt.Errorf("failed to load holidays: %w", err)
	}

	plan := &WorkPlan{
		Today: today,
	}
	plan.Calendar = NewTradingCalendar(holidays)

	if targetDate != "" {
		t, err := time.Parse("20060102", targetDate)
		if err != nil {
			return nil, fmt.Errorf("invalid target date format: %w", err)
		}
		plan.LastTradingDay = t
		plan.IsTradingTime = false

		// 补数模式下的节假日判定：如果是节假日，直接打断，不准执行后续任务
		if plan.Calendar != nil && (plan.Calendar.IsHoliday(t) || plan.Calendar.IsWeekend(t)) {
			plan.Reason = fmt.Sprintf("✅ %s 是节假日或周末，跳过", t.Format("2006-01-02"))
			return plan, nil
		}
	} else {
		plan.LastTradingDay = plan.Calendar.LastTradingDayOnOrBefore(today)
		plan.IsTradingTime = plan.Calendar.IsTradingTime(today)
	}

	// raw_holidays 为空：属于首次运行 / 旧库，放行所有任务让其自行写入节假日。
	if len(holidays) == 0 {
		plan.NeedDaily = true
		plan.NeedGbbq = true
		plan.NeedBasic = true
		plan.NeedFactor = true
		plan.NeedHolidays = true
		plan.NeedMin = min
		plan.Reason = "首次运行，全量同步"
		return plan, nil
	}

	dailyLatest, err := db.GetLatestDate(model.TableKlineDaily.TableName, "date")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest daily date: %w", err)
	}

	factorLatest, err := db.GetLatestDate(model.TableAdjustFactor.TableName, "date")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest factor date: %w", err)
	}

	now := time.Now().Local()
	nowTime := now.Hour()*100 + now.Minute()

	// 1. 收盘后更新是强制性的 (15:00 以后的运行)
	if targetDate == "" && nowTime >= 1500 {
		plan.NeedDaily = true
		plan.NeedGbbq = true
		plan.NeedBasic = true
		plan.NeedFactor = true
		plan.NeedHolidays = true
		plan.NeedMin = min
	} else if targetDate == "" && nowTime < 930 {
		// 2. 开盘前更新只看 gbbq (09:30 之前的运行)
		plan.NeedDaily = false
		plan.NeedMin = false
		plan.NeedGbbq = true
		plan.NeedBasic = true
		plan.NeedFactor = true
		plan.NeedHolidays = true
	} else {
		// 3. 其它时间 (日中或指定日期补数)
		if force {
			plan.NeedDaily = true
		} else if targetDate != "" {
			dateStr := plan.LastTradingDay.Format("2006-01-02")
			nextDayStr := plan.LastTradingDay.AddDate(0, 0, 1).Format("2006-01-02")
			exists, err := db.Exists(model.TableKlineDaily.TableName, "date >= ? AND date < ?", dateStr, nextDayStr)
			if err != nil {
				return nil, fmt.Errorf("failed to check daily data existence: %w", err)
			}
			plan.NeedDaily = !exists
		} else {
			plan.NeedDaily = dailyLatest.Before(plan.LastTradingDay)
		}

		// GBBQ 是否强制执行，通过独立的 gbbq 参数或 context 逻辑控制
		plan.NeedGbbq = gbbq
		
		// 检查复权因子是否需要重算 (如果没有显式要求 gbbq 且没有需要补的因子，就不重算)
		needFactorToday := false

		// 找出 LastTradingDay 之后的第 5 个未来交易日
		var futureDates []time.Time
		curr := plan.LastTradingDay
		for len(futureDates) < 5 {
			curr = curr.AddDate(0, 0, 1)
			if plan.Calendar.IsTradingDay(curr) {
				futureDates = append(futureDates, curr)
			}
		}
		targetLatestFactorDate := futureDates[4]

		if targetDate != "" {
			dateStr := targetLatestFactorDate.Format("2006-01-02")
			nextDayStr := targetLatestFactorDate.AddDate(0, 0, 1).Format("2006-01-02")
			exists, err := db.Exists(model.TableAdjustFactor.TableName, "date >= ? AND date < ?", dateStr, nextDayStr)
			if err != nil {
				return nil, fmt.Errorf("failed to check factor data existence: %w", err)
			}
			needFactorToday = !exists
		} else {
			needFactorToday = factorLatest.Before(targetLatestFactorDate)
		}

		// 只有在明确需要更新 GBBQ、或者未来交易日的复权因子缺失时，才运行因子重算 (不随 NeedDaily 联动)
		plan.NeedFactor = plan.NeedGbbq || needFactorToday

		plan.NeedHolidays = plan.NeedGbbq
		plan.NeedBasic = plan.NeedDaily || plan.NeedFactor

		if min {
			if force {
				plan.NeedMin = true
			} else if targetDate != "" {
				if missingOnly {
					plan.NeedMin = true
				} else {
					dateStr := plan.LastTradingDay.Format("2006-01-02")
					nextDayStr := plan.LastTradingDay.AddDate(0, 0, 1).Format("2006-01-02")
					exists, err := db.Exists(model.TableKline1Min.TableName,
						"datetime >= ? AND datetime < ?", dateStr, nextDayStr)
					if err != nil {
						return nil, fmt.Errorf("failed to check minute data existence: %w", err)
					}
					plan.NeedMin = !exists
				}
			} else {
				if missingOnly {
					plan.NeedMin = true
				} else {
					minLatest, err := db.GetLatestDate(model.TableKline1Min.TableName, "datetime")
					if err != nil {
						return nil, fmt.Errorf("failed to get latest minute date: %w", err)
					}
					plan.NeedMin = minLatest.Before(plan.LastTradingDay)
				}
			}
		}
	}

	// 根据用户显式指定的开关裁剪规划
	if !day {
		plan.NeedDaily = false
		plan.NeedBasic = false
	}
	if !gbbq {
		plan.NeedGbbq = false
		plan.NeedFactor = false
		plan.NeedHolidays = false
	}
	if !min {
		plan.NeedMin = false
	}

	plan.Reason = describePlan(plan, dailyLatest)
	return plan, nil
}

func describePlan(p *WorkPlan, dailyLatest time.Time) string {
	if !p.AnyNeeded() {
		return fmt.Sprintf("✅ %s 数据已存在，无需更新", p.LastTradingDay.Format("2006-01-02"))
	}
	if p.NeedDaily {
		return fmt.Sprintf("📅 日线需更新: %s (当前最新: %s)",
			p.LastTradingDay.Format("2006-01-02"), dailyLatest.Format("2006-01-02"))
	}
	if p.NeedMin {
		return fmt.Sprintf("📅 正在处理 %s 分钟线 (日线已就绪)",
			p.LastTradingDay.Format("2006-01-02"))
	}
	return "🚀 按计划执行任务"
}
