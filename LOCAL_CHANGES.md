# 本仓库的本地改动（请勿在上游同步时覆盖）

本目录是 `github.com/jing2uo/tdx2db` 的本地副本，相对上游有 **两处业务补丁**，
都是为了修复「TDX 证券名称被 16 字节字段截断」导致的名称缺失 / 乱码问题。
重新从上游同步代码时，请务必保留这两处改动（或把它们重新应用一遍）。

## 背景：名称为什么会坏

TDX 行情服务器接口 `0x44d`「在线代码名称列表」每条记录 37 字节，其中名称槽位是
**固定 16 字节 GBK**（`tdx/stock.go` 读取 `data[off+8 : off+24]`）。GBK 下 16 字节只
装得下 8 个汉字，或 6 个汉字 + `ETF`，于是超长名称在**数据源侧**就被砍断：

* 砍在汉字中间 → 末尾剩下半个 GBK 字节，解码时被替换成 `U+FFFD`，界面上就是
  「半个字符乱码」（如 `上证中盘ETF易方�`）；
* 砍在字符边界 → 没有乱码但同样缺字（如 `沪深300ETF华泰柏`，应为 `沪深300ETF华泰柏瑞`）。

## 补丁一：`gbkClean` 丢弃半个汉字（`tdx/block.go`）

新增 `trimIncompleteGBKTail()`：扫描 GBK 字节流，遇到**后继字节缺失的孤立前导字节就
截掉**，而不是交给解码器替换成 `U+FFFD`；并增加一层兜底去掉残留的替换字符。

效果：不再产生任何「半个字符乱码」。注意它**补不回缺失的字**（字节从未下发）。

配套测试：`tdx/gbk_truncate_test.go`。

## 补丁二：`update_symbol_names` 名称保优（`workflow/task_symbol.go`）

`raw_symbol_name` 每次同步都是 `TRUNCATE + 全量重导`，会把库里已有的完整名称打回
TDX 的截断版本。因此重导前先以代码为 Key 读出库中已有名称，做一次「保优」合并：
**行集与 `class` 取新值，名称则新旧择优**。

择优规则（优先级从高到低）：

| # | 条件 | 取值 | 用意 |
|---|---|---|---|
| 1 | 任一侧为空 | 另一侧 | 兜底 |
| 2 | 完全相同 | 新名 | 常态化 |
| 3 | 旧名带 U+FFFD、新名干净 | 新名 | 让补丁一能治好历史脏数据 |
| 4 | 新名带 U+FFFD、旧名干净 | 旧名 | 防御：不让乱码回流 |
| 5 | 旧名 = 新名 + 尾巴（前缀） | 旧名 | 新名被 16 字节槽位截断 |
| 6 | 新名顶到字段上限（GBK ≥ 15）且旧名更长 | 旧名 | 覆盖新旧命名口径不同、非前缀的情况 |
| 7 | 其余 | 新名 | 保证改名 / 扩位简称变更正常生效 |

> 注意：本表**不是**「一律保留旧名」——那样会导致改名永不生效、历史乱码被永久固化。
> 另外这里只修复「可证明被 16 字节字段损坏」的名称；股票名上的 `XD` / `*ST` / `N`
> 交易状态前缀属于正常口径，不做替换。

配套测试：`workflow/task_symbol_test.go`。

### 完整名称从哪来

补丁只负责「不把已有的好名称弄坏」，不负责生产完整名称。完整名称（交易所口径的
扩位简称）由使用方从数据商补写，例如本项目配套的：

```bash
cd trader/backend && ../.venv/bin/python scripts/repair_symbol_names_from_pg.py --apply
```

该脚本以 PostgreSQL `funds`（由东方财富 / akshare 同步而来）为权威来源回写
`raw_symbol_name`。**新上市的标的**首次出现时库里没有旧名，需再跑一次该脚本补齐。

## 构建

本仓库在远程服务器环境（如通过 rsync 同步的代码副本）中通常是
非 git 副本，且历史上残留过一个损坏的 `.git`（缺 `HEAD`/`config`），会让 go 的
VCS stamping 直接报错：

```
error obtaining VCS status: exit status 128
```

所以统一使用仓库根目录的 `build.sh` 构建（内部固定 `-buildvcs=false`）：

```bash
VERSION=dev+gbkfix+namekeep-20260929 ./build.sh          # 就地输出 ./tdx2db
OUT=/tmp/tdx2db-new VERSION=dev+xxx ./build.sh          # 输出到指定路径
```

构建完成后部署到服务目录（先备份、再原子替换）：

```bash
cd /path/to/trader/bin
cp -p tdx2db tdx2db.bak-$(date +%Y%m%d-%H%M%S)
cp /path/to/tdx2db/tdx2db tdx2db.new && chmod 775 tdx2db.new && mv -f tdx2db.new tdx2db
./tdx2db version
```

> 项目依赖的 duckdb 需要 cgo，跨平台构建需要对应交叉编译工具链，因此请在目标平台
> （Linux x86_64）上直接构建，不要从 macOS 交叉编译。
