package tdx

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"

	"github.com/jing2uo/tdx2db/model"
	"golang.org/x/text/encoding/simplifiedchinese"
)

type BlockType uint16

const (
	BlockTypeHY       BlockType = 0
	BlockTypeHY2      BlockType = 1
	BlockTypeGN       BlockType = 3
	BlockTypeFG       BlockType = 4
	BlockTypeDQ       BlockType = 5
	BlockTypeYJLevel1 BlockType = 7
	BlockTypeYJLevel2 BlockType = 8
	BlockTypeYJLevel3 BlockType = 9
)

// BlockListItem represents one entry from the 0x1231 block list response.
// Each 160-byte entry contains two 80-byte sub-records:
//   - [0:80]  the block itself (market, code, name, price data)
//   - [80:160] the block's representative/leading stock (market, code, name, price data)
type BlockListItem struct {
	Market uint16
	Code   string
	Name   string
	Query  BlockType

	// Representative stock of this block (offset 80+ of the 160-byte entry).
	// NOT parent block — TDX protocol does not include parent-child hierarchy.
	RepStockMarket uint16
	RepStockCode   string
}

type BlockMemberItem struct {
	Market uint16
	Code   string
	Name   string
}

// FetchOnlineBlocks fetches all block/industry/concept data and their members via OpenTDX protocol.
// Note: TDX 0x1231 protocol response does NOT contain parent-child hierarchy info.
// ParentCode in the returned BlockInfo is left empty; it must be derived separately
// (e.g., from membership overlap or block code conventions).
func FetchOnlineBlocks(ctx context.Context) ([]model.BlockInfo, []model.BlockMember, error) {
	client := NewOnlineClient()
	if err := client.Connect(); err != nil {
		return nil, nil, err
	}
	defer client.Close()

	queryTypes := []BlockType{BlockTypeGN, BlockTypeFG, BlockTypeDQ, BlockTypeYJLevel1, BlockTypeYJLevel2, BlockTypeYJLevel3}
	infoByCode := make(map[string]model.BlockInfo)
	memberSet := make(map[string]model.BlockMember)

	for _, qt := range queryTypes {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		blocks, err := client.GetBlockList(qt, 2000)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to fetch block list %s: %w", qt.blockType(), err)
		}
		for _, blockItem := range blocks {
			block := blockItem.toBlockInfo()
			if _, exists := infoByCode[block.BlockCode]; !exists {
				infoByCode[block.BlockCode] = block
			}

			members, err := client.GetBlockMembers(blockItem.Code, 5000)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to fetch members for %s %s: %w", blockItem.Code, blockItem.Name, err)
			}
			for _, m := range members {
				symbol, ok := marketCodeToSymbol(m.Market, m.Code)
				if !ok {
					continue
				}
				member := model.BlockMember{StockSymbol: symbol, BlockCode: block.BlockCode}
				memberSet[member.StockSymbol+"\x00"+member.BlockCode] = member
			}
		}
	}

	infos := make([]model.BlockInfo, 0, len(infoByCode))
	for _, v := range infoByCode {
		infos = append(infos, v)
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].BlockType == infos[j].BlockType {
			return infos[i].BlockSymbol < infos[j].BlockSymbol
		}
		return infos[i].BlockType < infos[j].BlockType
	})

	members := make([]model.BlockMember, 0, len(memberSet))
	for _, v := range memberSet {
		members = append(members, v)
	}
	sort.Slice(members, func(i, j int) bool {
		if members[i].BlockCode == members[j].BlockCode {
			return members[i].StockSymbol < members[j].StockSymbol
		}
		return members[i].BlockCode < members[j].BlockCode
	})

	return infos, members, nil
}

func (c *OnlineClient) GetBlockList(blockType BlockType, count int) ([]BlockListItem, error) {
	const pageSize = 150
	var result []BlockListItem
	for start := 0; start < count; start += pageSize {
		current := pageSize
		if count-start < current {
			current = count - start
		}
		items, err := c.getBlockListPage(blockType, uint16(start), uint16(current))
		if err != nil {
			return nil, err
		}
		result = append(items, result...)
		if len(items) < current {
			break
		}
	}
	return result, nil
}

// getBlockListPage fetches one page of the block list (command 0x1231).
// Response: 4-byte header + N × 160-byte entries.
// Each 160-byte entry = 80 bytes (block info) + 80 bytes (representative stock info).
// The 160-byte structure (verified against opentdx library):
//
//	fmt = "<H6s16s44sfff H6s16s44sfff"
//
//	Offset  Size  Field
//	0-1     2     Market (uint16 LE)
//	2-7     6     Code (6-byte GBK)
//	8-23    16    Reserved (all zeros)
//	24-67   44    Name (GBK, null-terminated)
//	68-71   4     Price (float32 LE)
//	72-75   4     Rise speed (float32 LE)
//	76-79   4     Pre-close (float32 LE)
//	80-81   2     RepStock Market (uint16 LE)
//	82-87   6     RepStock Code (6-byte GBK)
//	88-103  16    Reserved (all zeros)
//	104-147 44    RepStock Name (GBK, null-terminated)
//	148-151 4     RepStock Price (float32 LE)
//	152-155 4     RepStock Rise speed (float32 LE)
//	156-159 4     RepStock Pre-close (float32 LE)
func (c *OnlineClient) getBlockListPage(blockType BlockType, start, pageSize uint16) ([]BlockListItem, error) {
	body := make([]byte, 18)
	binary.LittleEndian.PutUint16(body[0:2], pageSize)
	binary.LittleEndian.PutUint16(body[2:4], uint16(blockType))
	body[4] = 0
	body[5] = 1
	binary.LittleEndian.PutUint16(body[6:8], start)
	binary.LittleEndian.PutUint16(body[8:10], 1)

	data, err := c.Call(0x1231, body)
	if err != nil {
		return nil, err
	}
	if len(data) < 4 {
		return nil, fmt.Errorf("block list response too short: %d", len(data))
	}
	countAll := int(binary.LittleEndian.Uint16(data[0:2]))
	rowCount := countAll / 2
	items := make([]BlockListItem, 0, rowCount)
	for i := 0; i < rowCount; i++ {
		off := 4 + i*160
		if off+160 > len(data) {
			break
		}
		market := binary.LittleEndian.Uint16(data[off : off+2])
		code := gbkClean(data[off+2 : off+8])
		name := gbkClean(data[off+24 : off+68])
		repStockMarket := binary.LittleEndian.Uint16(data[off+80 : off+82])
		repStockCode := gbkClean(data[off+82 : off+88])
		items = append(items, BlockListItem{
			Market:         market,
			Code:           code,
			Name:           name,
			Query:          blockType,
			RepStockMarket: repStockMarket,
			RepStockCode:   repStockCode,
		})
	}
	return items, nil
}

func (c *OnlineClient) GetBlockMembers(blockCode string, count int) ([]BlockMemberItem, error) {
	const pageSize = 80
	var result []BlockMemberItem
	for start := 0; start < count; start += pageSize {
		current := pageSize
		if count-start < current {
			current = count - start
		}
		items, err := c.getBlockMembersPage(blockCode, uint16(start), uint8(current))
		if err != nil {
			return nil, err
		}
		result = append(items, result...)
		if len(items) < current {
			break
		}
	}
	return result, nil
}

func (c *OnlineClient) getBlockMembersPage(blockCode string, start uint16, pageSize uint8) ([]BlockMemberItem, error) {
	body := make([]byte, 43)
	binary.LittleEndian.PutUint32(body[0:4], uint32(exchangeBlockCode(blockCode)))
	binary.LittleEndian.PutUint16(body[13:15], 0) // sort by code
	binary.LittleEndian.PutUint32(body[15:19], uint32(start))
	body[19] = pageSize
	body[20] = 0
	body[21] = 0 // no sort order
	body[22] = 0
	// body[23:43] bitmap remains all zero: request market/code/name only.

	data, err := c.Call(0x122c, body)
	if err != nil {
		return nil, err
	}
	if len(data) < 26 {
		return nil, fmt.Errorf("block member response too short: %d", len(data))
	}
	rowCount := int(binary.LittleEndian.Uint16(data[24:26]))
	items := make([]BlockMemberItem, 0, rowCount)
	for i := 0; i < rowCount; i++ {
		off := 26 + i*68
		if off+68 > len(data) {
			break
		}
		items = append(items, BlockMemberItem{
			Market: binary.LittleEndian.Uint16(data[off : off+2]),
			Code:   gbkClean(data[off+2 : off+24]),
			Name:   gbkClean(data[off+24 : off+68]),
		})
	}
	return items, nil
}

// toBlockInfo converts a BlockListItem to model.BlockInfo.
// ParentCode is left empty because the TDX 0x1231 protocol does not provide
// parent-child hierarchy info. Derive it separately if needed.
func (b BlockListItem) toBlockInfo() model.BlockInfo {
	blockType, level := b.Query.blockType(), b.Query.level()
	blockSymbol, _ := marketCodeToSymbol(b.Market, b.Code)
	if blockSymbol == "" {
		blockSymbol = "sh" + b.Code
	}
	var repStockCode string
	if b.RepStockCode != "" {
		if repSymbol, ok := marketCodeToSymbol(b.RepStockMarket, b.RepStockCode); ok {
			repStockCode = repSymbol
		}
	}
	return model.BlockInfo{
		BlockType:    blockType,
		BlockName:    b.Name,
		BlockSymbol:  blockSymbol,
		BlockCode:    blockSymbol,
		ParentCode:   "",
		BlockLevel:   level,
		RepStockCode: repStockCode,
	}
}

func (b BlockType) blockType() string {
	switch b {
	case BlockTypeGN:
		return "concept"
	case BlockTypeFG:
		return "style"
	case BlockTypeDQ:
		return "region"
	case BlockTypeYJLevel1, BlockTypeYJLevel2, BlockTypeYJLevel3:
		return "tdx_research"
	case BlockTypeHY, BlockTypeHY2:
		return "tdx_research"
	default:
		return "unknown"
	}
}

func (b BlockType) level() int {
	switch b {
	case BlockTypeYJLevel1:
		return 1
	case BlockTypeYJLevel2:
		return 2
	case BlockTypeYJLevel3:
		return 3
	default:
		return 1
	}
}

// DeriveParentCodesByOrder derives parent-child relationships based on the
// hypothesis that TDX YJ blocks are ordered such that:
//   - Level2 blocks immediately following a Level1 block in code-numeric order
//     are children of that Level1 block
//   - Level3 blocks immediately following a Level2 block are children of that Level2 block
//
// Algorithm: sort YJ blocks by numeric code value, then scan:
// keep "lastSeenLevel1" and "lastSeenLevel2" pointers; assign parent on encounter.
func DeriveParentCodesByOrder(infos []model.BlockInfo) map[string]string {
	// Filter and sort YJ research blocks by numeric code
	type item struct {
		symbol string
		level  int
		num    int // numeric portion of block code
	}
	var items []item
	for _, info := range infos {
		if info.BlockType != "tdx_research" {
			continue
		}
		n := blockCodeNumeric(info.BlockSymbol)
		items = append(items, item{symbol: info.BlockSymbol, level: info.BlockLevel, num: n})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].num < items[j].num
	})

	parentOf := make(map[string]string)
	var lastLevel1, lastLevel2 string

	for _, it := range items {
		switch it.level {
		case 1:
			lastLevel1 = it.symbol
			lastLevel2 = "" // reset: new Level1 starts a new subtree
		case 2:
			if lastLevel1 != "" {
				parentOf[it.symbol] = lastLevel1
			}
			lastLevel2 = it.symbol
		case 3:
			if lastLevel2 != "" {
				parentOf[it.symbol] = lastLevel2
			} else if lastLevel1 != "" {
				parentOf[it.symbol] = lastLevel1
			}
			// Level3 does NOT update lastLevel2 — it's a leaf
		}
	}

	return parentOf
}

// blockCodeNumeric extracts the numeric portion of a block symbol (e.g. "sh881385" → 881385).
func blockCodeNumeric(symbol string) int {
	s := strings.TrimSpace(symbol)
	// Strip known prefixes
	for _, p := range []string{"sh", "sz", "bj"} {
		if strings.HasPrefix(s, p) {
			s = s[len(p):]
			break
		}
	}
	return atoiSafe(s)
}

// DeriveParentCodes computes parent-child relationships from membership overlap.
// For each Level2 block, finds the most specific Level1 superset;
// for each Level3 block, finds the most specific Level2 superset.
// Only processes YJ (tdx_research) blocks with hierarchy.
// Returns a map of block_symbol → parent_block_symbol (e.g. "sh881386" → "sh881385").
func DeriveParentCodes(infos []model.BlockInfo, members []model.BlockMember) map[string]string {
	// Build member sets: block_code → set of stock symbols
	memberSet := make(map[string]map[string]struct{})
	for _, m := range members {
		if memberSet[m.BlockCode] == nil {
			memberSet[m.BlockCode] = make(map[string]struct{})
		}
		memberSet[m.BlockCode][m.StockSymbol] = struct{}{}
	}

	// Index blocks by symbol for level lookup
	bySymbol := make(map[string]model.BlockInfo)
	for _, info := range infos {
		bySymbol[info.BlockSymbol] = info
	}

	parentOf := make(map[string]string)

	for _, info := range infos {
		if info.BlockType != "tdx_research" || info.BlockLevel <= 1 {
			continue
		}
		childMembers := memberSet[info.BlockCode]
		if len(childMembers) == 0 {
			continue
		}

		var bestParent string
		bestCount := int(^uint(0) >> 1) // max int

		for _, candidate := range infos {
			if candidate.BlockType != "tdx_research" {
				continue
			}
			if candidate.BlockLevel != info.BlockLevel-1 {
				continue
			}
			if candidate.BlockCode == info.BlockCode {
				continue
			}
			parentMembers := memberSet[candidate.BlockCode]
			if len(parentMembers) == 0 || len(parentMembers) < len(childMembers) {
				continue
			}
			// Check if every stock in child is also in candidate parent
			allContained := true
			for s := range childMembers {
				if _, ok := parentMembers[s]; !ok {
					allContained = false
					break
				}
			}
			if allContained && len(parentMembers) < bestCount {
				bestParent = candidate.BlockSymbol
				bestCount = len(parentMembers)
			}
		}

		if bestParent != "" {
			parentOf[info.BlockSymbol] = bestParent
		}
	}

	return parentOf
}

func marketCodeToSymbol(market uint16, code string) (string, bool) {
	switch market {
	case 0:
		return "sz" + code, true
	case 1:
		return "sh" + code, true
	case 2:
		return "bj" + code, true
	default:
		return "", false
	}
}

func exchangeBlockCode(blockSymbol string) int {
	s := strings.TrimSpace(blockSymbol)
	switch {
	case strings.HasPrefix(s, "US"):
		return 30000 + atoiSafe(strings.TrimPrefix(s, "US"))
	case strings.HasPrefix(s, "HK"):
		return 20000 + atoiSafe(strings.TrimPrefix(s, "HK"))
	case strings.HasPrefix(s, "sh") || strings.HasPrefix(s, "sz") || strings.HasPrefix(s, "bj"):
		return exchangeBlockCode(s[2:])
	case strings.HasPrefix(s, "000"):
		return 31000 + atoiSafe(s)
	case strings.HasPrefix(s, "399") && len(s) == 6:
		return atoiSafe(s) - 399000 + 30000
	case strings.HasPrefix(s, "899") && len(s) == 6:
		return atoiSafe(s) - 899000 + 32000
	case strings.HasPrefix(s, "88") && len(s) == 6:
		return atoiSafe(s) - 880000 + 20000
	default:
		return atoiSafe(s)
	}
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// trimIncompleteGBKTail 丢掉尾部不完整的 GBK 双字节序列。
//
// TDX 0x44d 接口的名称槽位固定 16 字节（tdx/stock.go 读取 data[off+8:off+24]），
// GBK 下 16 字节只装得下 8 个汉字或 6 个汉字 + "ETF"，超长名称在数据源侧就被按字节
// 砍断，末尾可能只剩一个孤立的 GBK 前导字节。这种残缺序列若直接交给解码器会被替换成
// U+FFFD，在名称里表现为「半个汉字」乱码（例如「上证中盘ETF易方\ufffd」），
// 因此这里先把它切掉，只保留能完整解码的部分。
func trimIncompleteGBKTail(raw []byte) []byte {
	for i := 0; i < len(raw); {
		b := raw[i]
		switch {
		case b < 0x80: // ASCII 单字节
			i++
		case b >= 0x81 && b <= 0xFE: // GBK 前导字节，需成对消费
			if i+1 >= len(raw) {
				return raw[:i] // 后继字节缺失：截掉这半个字
			}
			i += 2
		default: // 0x80 / 0xFF 等非法单字节，原样交给解码器
			i++
		}
	}
	return raw
}

func gbkClean(raw []byte) string {
	if i := bytes.IndexByte(raw, 0); i >= 0 {
		raw = raw[:i]
	}
	raw = trimIncompleteGBKTail(raw)
	out, err := simplifiedchinese.GBK.NewDecoder().Bytes(raw)
	if err != nil {
		return strings.TrimSpace(string(raw))
	}
	// 兜底：源数据本身就带替换字符时一并去掉，保证入库名称不再出现半个字
	return strings.TrimSpace(strings.ReplaceAll(string(out), "\uFFFD", ""))
}
