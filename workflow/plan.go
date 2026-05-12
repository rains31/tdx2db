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

	IsTradingTime  bool
	Reason         string // 用于日志
}

// AnyNeeded 是否有任何任务需要执行。
func (p *WorkPlan) AnyNeeded() bool {
	return p.NeedDaily || p.NeedGbbq || p.NeedBasic || p.NeedFactor || p.NeedHolidays || p.NeedMin
}

// BuildWorkPlan 读取交易日历与各表最新日期，推导本次 cron 要做什么。
func BuildWorkPlan(db database.DataRepository, today time.Time, min bool, targetDate string) (*WorkPlan, error) {
	plan := &WorkPlan{
		Today: today,
	}

	holidays, err := db.GetHolidays()
	if err != nil {
		return nil, fmt.Errorf("failed to get holidays: %w", err)
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

	// 增量同步逻辑：日线
	plan.NeedDaily = dailyLatest.Before(plan.LastTradingDay)
	plan.NeedGbbq = plan.NeedDaily
	plan.NeedBasic = plan.NeedDaily
	plan.NeedFactor = plan.NeedDaily
	plan.NeedHolidays = plan.NeedDaily

	if min {
		if targetDate != "" {
			// 如果指定了日期，检查该日期是否已经有数据
			dateStr := plan.LastTradingDay.Format("2006-01-02")
			nextDayStr := plan.LastTradingDay.AddDate(0, 0, 1).Format("2006-01-02")
			exists, err := db.Exists(model.TableKline1Min.TableName,
				"datetime >= ? AND datetime < ?", dateStr, nextDayStr)
			if err != nil {
				return nil, fmt.Errorf("failed to check minute data existence: %w", err)
			}
			plan.NeedMin = !exists
		} else {
			minLatest, err := db.GetLatestDate(model.TableKline1Min.TableName, "datetime")
			if err != nil {
				return nil, fmt.Errorf("failed to get latest minute date: %w", err)
			}
			// 仅当分钟线落后于最近交易日时才触发更新
			plan.NeedMin = minLatest.Before(plan.LastTradingDay)
		}
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
