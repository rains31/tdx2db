<p align="center">
<img src="./tdx2db.png" width="300"/>
</p>

# tdx2db - 获得专属的 A 股行情数据库（增强版）

[![GitHub release](https://img.shields.io/github/v/release/jing2uo/tdx2db?style=flat-square)](https://github.com/jing2uo/tdx2db/releases)
[![License](https://img.shields.io/github/license/jing2uo/tdx2db?style=flat-square)](LICENSE)

将通达信行情数据导入本地数据库，原生支持 **ClickHouse** 与 **DuckDB**。
在原版基础上进行了深度重构与增强，支持可转债、板块指数、全数据类型独立开关、库内差集幂等补数与安全防误删机制。

## 亮点与增强特性

- **多资产全品种覆盖**：股票、ETF、指数，以及新增的**沪深可转债**与**通达信行业/概念板块指数**
- **细粒度按需任务开关**：支持 `--day`、`--min`、`--basic`、`--block`、`--symbol`、`--gbbq` 独立执行或组合执行，大幅提升历史补数吞吐
- **库内差集去重（方案 A）**：基于临时表与 `ANTI JOIN` 精准识别缺失行，杜绝重复插入，实现零冗余与完全幂等
- **安全防误删设计**：解耦 `--force` 与 `--delete`，默认永不清除已有数据，杜绝历史回填时的破坏性误删
- **衍生与复权解耦**：自动计算后复权因子、前收盘价、换手率、市值，并支持独立单次批量运行
- **在线数据与乱码防护**：基于 opentdx 协议拉取代码名称与板块成员，内置 GBK 截断清理与名称新旧择优机制
- **稳定高并发**：修复多进程并发释放提取工具时的 `text file busy` 锁冲突，支持原生 Go 与高并发环境

---

## 🚀 相对原版的核心改进

本版本重点解决了原版在**可转债支持缺失**、**历史回填流程臃肿**、**数据重复膨胀**、**误删历史**以及**高并发稳定性**等核心痛点：

### 1. 标的品类扩展与精度修正
* **可转债全流程支持 (`2e37c88`)**：扩展对沪深可转债（`sh11`、`sz12`、`sz13`）的日线与 1 分钟分时数据的识别、转档导入及 `bond` 品类分类。
* **可转债价格精度修复 (`c5a75da`)**：通达信可转债内部报价精度为万分之一（元），将解析时的 `PriceScale` 修正为 `10000.0`，修复了原版可转债价格被放大 100 倍的严重 Bug。
* **行业与概念板块日线采集 (`9df43ed`)**：支持通达信板块指数日线（`sh880xxx` / `sh881xxx`）采集；针对板块总成交量常超数百亿股的特性，专门跳过普通个股的手单位溢出换算，防止成交量失真。

### 2. 细粒度数据类型开关与历史高效补数
* **全数据类型独立开关 (`da70151`)**：将原本强行绑定的全量更新拆分为独立任务开关：`--all`（全量）、`--day`（日线）、`--min`（分钟线）、`--basic`（衍生指标）、`--block`（板块）、`--symbol`（代码名称）、`--gbbq`（股本复权）。当传入特定开关时只跑对应任务，避免无谓开销。
* **基础行情指标计算独立解耦 (`a9f2b1e`)**：`raw_basic_daily`（前收盘价/换手率/市值等）计算需基于全历史 K 线和股本重算，耗时较长。拆分出独立的 `--basic` 开关后，在回填数月或数年的历史场景下，可仅执行 `--day --min` 极速入库，最后单次执行 `--basic` 进行全量指标计算，避免了“每天回填都做一次全量重算”的算力浪费。
* **复权因子与股本变迁彻底解耦 (`1111515`, `e624d56`)**：使 `factor` 与 `gbbq` 真正独立于日线导入流程，避免补历史日线时被强制绑定重复拉取与覆盖股本。

### 3. 库内临时表差集去重与安全防误删
* **库内差集精准去重落地（方案 A） (`096df57`)**：利用数据库临时表（ClickHouse: `_tmp_kline_*` / DuckDB 内存临时表）暂存导入批次，在库内执行 `LEFT ANTI JOIN`（或 `ANTI JOIN`）比对已存在主键（代码 + 日期/时间），仅将缺失数据流式写入目标表，彻底解决数据重复插入膨胀问题。
* **默认开启缺失补齐 (`06bcf26`)**：将 `--missing-only` 开关默认值设为 `true`，无论单日增量还是历史回填，均保证幂等性。
* **安全防误删解耦 (`20be2ad`)**：将 `--force` 与 `--delete` 彻底拆分。`--force` 仅用于强制放行工作流规划（跳过最新日期等前置检查），默认绝不删除数据；如需物理清除指定日期数据重新导入，必须显式传递 `--delete`。

### 4. 数据解析与高并发鲁棒性
* **并发释放 datatool 互斥锁 (`84612f5`)**：在解包释放底层 Linux 二进制工具时增加全局互斥文件锁与存在校验，彻底杜绝多进程或并发调用时的 `text file busy` 启动报错。
* **跨日比对与时间解析修正 (`7b470dc`, `ed0e7c7`)**：排重 Key 加入完整日期维度，避免跨日同一时间点分时误判重复；修复 CSV datetime 字符串解析错误导致的漏检问题。
* **超长证券名称截断与乱码修复 (`LOCAL_CHANGES`)**：修复 TDX 16 字节槽位超长截断造成的乱码（`U+FFFD`），并在名称重导时实施新旧择优合并，防止数据商修正的完整名称被 TDX 截断版本覆盖。

---

## 安装

### 二进制下载

从 [releases](https://github.com/jing2uo/tdx2db/releases) 下载对应平台压缩包，解压后安装至系统路径：

```bash
sudo mv tdx2db /usr/local/bin/ && tdx2db -h
```

提供 Linux (amd64/arm64)、macOS (arm64)、Windows (amd64) 预编译版本。

### 本地编译

因依赖 cgo (DuckDB)，推荐在目标系统下通过统一构建脚本进行编译：

```bash
# 本地构建（自动关闭 VCS Stamping，避免缺失 git 报错）
./build.sh

# 指定输出路径与版本号
OUT=/usr/local/bin/tdx2db VERSION=dev+enhanced ./build.sh
```

---

## 使用指南

### 1. 首次初始化 (`init`)

首次使用需全量导入历史日线数据。从[通达信券商数据中心](https://www.tdx.com.cn/article/vipdata.html)下载 **沪深京日线数据完整包** (`hsjday.zip`) 并解压至 `vipdoc` 目录：

```bash
# 1. 准备日线数据
mkdir -p vipdoc
wget https://data.tdx.com.cn/vipdoc/hsjday.zip && unzip -q hsjday.zip -d vipdoc

# 2. 全量初始化导入
# DuckDB:
tdx2db init --dburi 'duckdb://./tdx.db' --dayfiledir ./vipdoc

# ClickHouse:
# 格式: clickhouse://[user[:password]@][host][:port][/database][?http_port=...]
tdx2db init --dburi 'clickhouse://localhost' --dayfiledir ./vipdoc
```

### 2. 日常增量更新 (`cron`)

`cron` 负责日常数据的增量同步。初次 `init` 后建议先执行一次完整同步：

```bash
# 默认全量增量（日线、股本、代码名称、板块、日历等）
tdx2db cron --dburi 'clickhouse://localhost'

# 包含 1 分钟分时数据的全量同步
tdx2db cron --dburi 'clickhouse://localhost' --min
```

### 3. 精细化按需任务示例

通过细粒度开关，只执行特定数据任务，避免无用开销：

```bash
# 仅更新日线与 1 分钟分时数据
tdx2db cron --dburi 'clickhouse://localhost' --day --min

# 仅更新在线代码名称与板块成分信息
tdx2db cron --dburi 'clickhouse://localhost' --symbol --block

# 仅更新股本变迁与复权数据
tdx2db cron --dburi 'clickhouse://localhost' --gbbq

# 仅计算并重刷基础行情衍生指标 (raw_basic_daily)
tdx2db cron --dburi 'clickhouse://localhost' --basic
```

### 4. 历史回填与补数最佳实践 (Best Practice)

当需要回填数月或数年的历史缺失数据时，**推荐采用两阶段流水线**：

```bash
# 阶段 1：循环补历史日线与分钟线（使用 --day --min 跳过基础指标每日重算，极大提速）
tdx2db cron --dburi 'clickhouse://localhost' --date 20261015 --day --min --force
tdx2db cron --dburi 'clickhouse://localhost' --date 20261016 --day --min --force
...

# 阶段 2：历史 K 线回填完毕后，单次全量重算基础行情指标
tdx2db cron --dburi 'clickhouse://localhost' --basic
```

> **注意**：
> - 默认已开启 `--missing-only=true`，若数据已在库中会自动跳过。
> - 如果确认某天数据损坏需要**清空重导**，请显式附带 `--delete`：
>   ```bash
>   tdx2db cron --dburi 'clickhouse://localhost' --date 20261030 --day --min --delete --force
>   ```

---

## 命令行参数一览

### 全局参数 (Global Flags)

| 参数 | 类型 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `--temp` | string | `""` | 临时文件父目录，留空使用系统 `$TMPDIR`（在磁盘 tmpfs 空间不足时非常实用） |
| `-v, --version` | bool | `false` | 输出版本号与构建 Commit 信息 |
| `-h, --help` | - | - | 查看命令帮助 |

### `init` 命令参数

| 参数 | 类型 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `--dburi` | string | **必填** | 数据库连接字符串（ClickHouse 或 DuckDB） |
| `--dayfiledir` | string | **必填** | 通达信解压后的 `vipdoc` 日线文件目录 |

### `cron` 命令参数

| 参数 | 类型 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `--dburi` | string | **必填** | 数据库连接字符串 |
| `--all` | bool | `false` | 显式声明导入所有类型数据（全流程同步） |
| `--day` | bool | `false` | 导入日线数据（股票 / ETF / 可转债 / 指数 / 板块） |
| `--min` | bool | `false` | 导入 1 分钟分时数据（股票 / ETF / 可转债） |
| `--basic` | bool | `false` | 计算并更新基础行情衍生指标（`raw_basic_daily`：市值/换手率/前收盘价等） |
| `--block` | bool | `false` | 导入在线板块、行业、概念分类及成分数据 |
| `--symbol` | bool | `false` | 导入在线代码名称与资产分类数据（带乱码修复与保优合并） |
| `--gbbq` | bool | `false` | 下载并导入 `gbbq` 股本变迁与复权因子计算 |
| `--date` | string | `""` | 指定同步的目标日期（格式：`YYYYMMDD`，可选） |
| `--missing-only`| bool | `true` | **库内差集补数**：基于临时表与 `ANTI JOIN` 仅补全缺失数据，不重复插入已有数据 |
| `--force` | bool | `false` | **强制放行规划**：跳过目标日期等前置检查；**默认不删除历史数据** |
| `--delete` | bool | `false` | **先清后导**：导入前物理删除指定 `--date` 的已有数据（防止脏数据残留） |
| `--vipdocdir` | string | `""` | 通达信 vipdoc 本地目录（可选，留空使用内置缓存） |

> **提示**：未显式传递任何数据类型开关（`--day`/`--min`/`--basic`/`--block`/`--symbol`/`--gbbq`）时，默认以全量工作流模式运行；一旦显式指定了任意一个分类开关，则严格按需只运行指定的任务。

---

## 数据库表与视图

`raw_` 前缀为持久化基础数据，`v_` 前缀为复权视图。

| 表 / 视图 | 说明 | 资产分类 |
| :--- | :--- | :--- |
| `_meta` | schema 版本等元信息 | 系统 |
| `raw_kline_daily` | 日线表（包含 O/H/L/C、成交量、成交额） | 股票 / ETF / 指数 / 可转债 / 板块指数 |
| `raw_kline_1min` | 1 分钟分时 K 线表 | 股票 / ETF / 可转债 |
| `raw_basic_daily` | 基础行情衍生表（前收盘价、换手率、流通市值、总市值、振幅等） | 股票 / ETF |
| `raw_adjust_factor` | 后复权因子表 | 股票 / ETF |
| `raw_gbbq` | 通达信股本变迁与分红配股数据 | 股票 / ETF |
| `raw_holidays` | A 股交易日历与休市节假日 | 系统 |
| `raw_symbol_class` | 资产代码与品类映射 (`stock`/`etf`/`index`/`bond`/`block`) | 全品种 |
| `raw_symbol_name` | 证券在线代码与名称表 | 全品种 |
| `raw_tdx_blocks_info` | 在线板块 / 概念 / 行业信息 | 板块 |
| `raw_tdx_blocks_member`| 板块与标的成分关联关系 | 板块 |
| `v_stock_{bfq,qfq,hfq}` | 股票 不复权 / 前复权 / 后复权日线视图 | 股票 |
| `v_etf_{bfq,qfq,hfq}` | ETF 不复权 / 前复权 / 后复权日线视图 | ETF |

### 复权视图与扩展品种查询示例

```sql
-- 股票前复权日线
SELECT * FROM v_stock_qfq WHERE symbol = 'sz000001' ORDER BY date;

-- ETF 后复权日线
SELECT * FROM v_etf_hfq WHERE symbol = 'sh510300' ORDER BY date;

-- 查询可转债日线 (sh11/sz12/sz13)
SELECT * FROM raw_kline_daily WHERE symbol LIKE 'sh11%' OR symbol LIKE 'sz12%' ORDER BY date DESC;

-- 查询板块指数行情 (sh880/sh881)
SELECT * FROM raw_kline_daily WHERE symbol LIKE 'sh880%' ORDER BY date DESC;
```

---

## 声明 & 支持项目

- 当前代码不做历史兼容，也可能写出 bug，请自行检查数据正确性。
- 导入分时后请保留原始数据并定期备份，日线可快速重建，分时不行。
- 使用问题欢迎来 [Telegram](https://t.me/tdx2db) 讨论。

tdx2db 永远免费开源，日线 + 分时 + 复权 + 板块足够跑基本的量化研究。如果你确实需要**逐笔成交**、**高频历史数据**和更多配套维度，可以考虑付费订阅 [Trade Data Plus（tdp）](https://tdp-site.pages.dev/)，提供：

- **逐笔成交（tick）**：全市场 3 秒级逐笔，含 B / S / X 主动买卖方向判定
- **非聚合 1min / 5min**：原始分时档，不是二次聚合版本
- **港股日线 + 复权因子**：A 股之外覆盖全市场港股
- **指数 / 行业 / 概念成分**：中证 300 / 500 / 1000 / 2000 加通达信板块及成分股映射
- **证券基础信息**：代码 ↔ 名称、品种分类、A 股上市 / 退市日期、港股标的信息
- **历史包**：逐笔、分钟线等高频历史数据一次性导入（2024 元旦至今）

同样落进你自己的 ClickHouse，可查询、可导出、可回测。tdp 提供更原生的跨平台支持：Linux / macOS / Windows 单二进制、`install.sh` 自动识别架构，安装与每日增量同步流程一致。细节见[网站](https://tdp-site.pages.dev/)。

---

## 致谢

- 在线代码名称、板块 / 行业 / 概念数据的协议处理与解析逻辑，参考并转写自 [opentdx](https://github.com/LisonEvf/opentdx)，让 tdx2db 能以 Go 版本接入这部分在线数据。
- Windows、macOS 和 Linux arm64 下的日线合并由 [@Abelonx](https://github.com/Abelonx) 在 [#60](https://github.com/jing2uo/tdx2db/pull/60) 贡献的 native Go 实现支持，让 tdx2db 摆脱了对 Linux amd64 datatool 二进制的依赖。
- QUANTAXIS 提供的复权算法参考。

## 欢迎 issue 和 pr

有任何使用问题都可以开 issue 讨论，也期待 pr~
