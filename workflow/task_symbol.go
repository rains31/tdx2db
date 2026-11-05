package workflow

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/tdx"
	"github.com/jing2uo/tdx2db/utils"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// symbolNameFieldBytes 是 TDX 行情协议里证券名称槽位的固定字节数
// （tdx/stock.go 读取 data[off+8 : off+24]，GBK 编码）。
const symbolNameFieldBytes = 16

var TaskUpdateSymbolNames *Task

func init() {
	TaskUpdateSymbolNames = &Task{
		Name:      "update_symbol_names",
		DependsOn: []string{},
		Executor:  executeUpdateSymbolNames,
		OnError:   ErrorModeSkip,
	}
	registerTask(TaskUpdateSymbolNames, "update")
}

func executeUpdateSymbolNames(ctx context.Context, db database.DataRepository, args *TaskArgs) (*TaskResult, error) {
	fmt.Println("🧩 开始下载代码名称")

	names, err := tdx.FetchOnlineSymbolNames(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch online symbol names: %w", err)
	}

	// 本表每次都是 TRUNCATE + 全量重导，而 TDX 行情协议的名称槽位固定 16 字节 GBK
	// （见 tdx/stock.go），超长名称在数据源侧就被截断。库里可能已经存有更完整的名称
	// （由使用方从交易所/数据商的扩位简称补写），直接覆盖会把它们打回截断版本，
	// 因此这里以代码为 Key 做一次「保优」合并：行集与 class 取新值，名称则新旧择优。
	preserved := 0
	if existing, err := loadExistingSymbolNames(db); err != nil {
		// 读旧值失败不应阻断同步（首次运行、表刚建等）
		fmt.Printf("⚠️  读取已有代码名称失败，按全量覆盖处理: %v\n", err)
	} else if len(existing) > 0 {
		names, preserved = mergeSymbolNames(existing, names)
	}

	csvPath := filepath.Join(args.TempDir, "symbol_name.csv")
	writer, err := utils.NewCSVWriter[model.SymbolName](csvPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create symbol name CSV writer: %w", err)
	}
	if err := writer.Write(names); err != nil {
		writer.Close()
		return nil, err
	}
	writer.Close()

	if err := db.ImportSymbolNames(csvPath); err != nil {
		return nil, fmt.Errorf("failed to import symbol name csv: %w", err)
	}

	msg := fmt.Sprintf("symbol names imported: %d rows (%d names kept from db)", len(names), preserved)
	if preserved > 0 {
		fmt.Printf("♻️  保留库中更完整的名称 %d 条（未被 16 字节槽位截断的版本覆盖）\n", preserved)
	}
	fmt.Printf("🚀 代码名称导入成功\n")
	return &TaskResult{State: StateCompleted, Rows: len(names), Message: msg}, nil
}

// loadExistingSymbolNames 读取当前库中的代码名称，用于重导前的保优合并。
func loadExistingSymbolNames(db database.DataRepository) ([]model.SymbolName, error) {
	var existing []model.SymbolName
	if err := db.Query(model.TableSymbolName.TableName, nil, &existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// preferredSymbolName 在旧名（库中已有）与新名（本次从 TDX 拉到）之间择优。
//
// TDX 行情协议的名称槽位固定 16 字节 GBK（见 tdx/stock.go），超长名称在数据源侧就被
// 砍断：要么恰好停在字符边界（静默截断），要么把末字劈成半个（由 gbkClean 丢掉的
// 15 字节）。因此「新名接近 16 字节且旧名更长」就是被截断的强信号。
//
// 规则（按优先级）：
//  1. 任一侧为空 → 取另一侧；
//  2. 完全相同 → 取新名（去掉首尾空白）；
//  3. 旧名带 U+FFFD 而新名干净 → 取新名，让解码修复能生效；
//  4. 新名带 U+FFFD 而旧名干净 → 保旧名，避免脏数据回流；
//  5. 旧名以新名为前缀且更长 → 新名被截掉了尾巴，保旧名；
//  6. 新名已顶到 16 字节字段上限（GBK ≥ 15）且旧名更长 → 视为被截断，保旧名
//     （覆盖「新旧命名口径不同、并非前缀关系」的情况，如 TDX 的 港股通信息技术ET
//     对应完整名 易方达中证港股通信息技术综合ETF）；
//  7. 其余情况取新名，保证改名、扩位简称变更等能正常生效。
func preferredSymbolName(oldName, newName string) string {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)

	oldRunes := utf8.RuneCountInString(oldName)
	newRunes := utf8.RuneCountInString(newName)

	switch {
	case oldName == "":
		return newName
	case newName == "":
		return oldName
	case oldName == newName:
		return newName
	case strings.ContainsRune(oldName, '\uFFFD') && !strings.ContainsRune(newName, '\uFFFD'):
		return newName
	case strings.ContainsRune(newName, '\uFFFD') && !strings.ContainsRune(oldName, '\uFFFD'):
		return oldName
	case oldRunes <= newRunes:
		return newName
	case strings.HasPrefix(oldName, newName):
		return oldName
	case gbkLen(newName) >= symbolNameFieldBytes-1:
		return oldName
	default:
		return newName
	}
}

// gbkLen 返回字符串的 GBK 字节长度，用于判断是否顶到 TDX 名称槽位上限。
func gbkLen(s string) int {
	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
	if err != nil {
		return len(s)
	}
	return len(encoded)
}

// mergeSymbolNames 把库中已有的名称合并进本次拉取结果，返回合并后的切片与保旧的条数。
// 行集以 incoming 为准（TDX 是标的范围的权威），class 取新值，名称按 preferredSymbolName 择优。
// tdxToQmt 机械转换 TDX 代码 -> QMT (sh600519 -> 600519.SH)。
// 与 trader 侧 utils/symbol_fmt.to_qmt 的 TDX<->QMT 规则保持一致。
// THS 码无法由 TDX 推导 (指数 USHI1A0001 <-> 000001.SH 非机械对应), 只能沿用旧表值。
func tdxToQmt(symbol string) string {
	s := strings.TrimSpace(symbol)
	if len(s) != 8 {
		return ""
	}
	mkt := strings.ToUpper(s[:2])
	code := s[2:]
	if mkt != "SH" && mkt != "SZ" && mkt != "BJ" {
		return ""
	}
	for _, r := range code {
		if !((r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')) {
			return ""
		}
	}
	return strings.ToUpper(code) + "." + mkt
}

func mergeSymbolNames(existing []model.SymbolName, incoming []model.SymbolName) ([]model.SymbolName, int) {
	oldNames := make(map[string]string, len(existing))
	oldThs := make(map[string]string, len(existing))
	for _, item := range existing {
		if item.Symbol != "" {
			oldNames[item.Symbol] = item.Name
			oldThs[item.Symbol] = item.ThsSymbol
		}
	}

	preserved := 0
	merged := make([]model.SymbolName, 0, len(incoming))
	for _, item := range incoming {
		oldName := oldNames[item.Symbol]
		chosen := preferredSymbolName(oldName, item.Name)
		if chosen != strings.TrimSpace(item.Name) && chosen != "" {
			preserved++
		}
		item.Name = chosen
		// 代码格式两列: qmt_symbol 机械推导兜底, ths_symbol 沿用旧表 (THS 码不可推导)
		if item.QmtSymbol == "" {
			item.QmtSymbol = tdxToQmt(item.Symbol)
		}
		item.ThsSymbol = oldThs[item.Symbol]
		merged = append(merged, item)
	}
	return merged, preserved
}
