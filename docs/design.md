# 本地交易图风险分析与交易模拟 — 设计说明

## 1. 目标与范围

用本地账本数据模拟链上资金流，在**重复投递、乱序到达、字段缺失、类型非法**等
非理想输入下给出**稳定、确定、可解释**的风险结论，并支持资金模拟与风险规则的
版本化更新、回放。全部能力为纯本地实现，无外部依赖、无网络 IO。

包结构：

| 包 | 职责 |
|---|---|
| `internal/ledger` | 本地账本的增量摄取、规范化、去重与拒绝原因分类 |
| `internal/graph` | 由交易构建只读有向图，识别汇聚/拆分/环路/异常集中 |
| `internal/risk` | 规则集版本化、确定性评分、并发分析、规则回放 |
| `internal/sim` | 账户余额推导、单笔/链式资金模拟、超深防护 |
| `internal/e2e` | 端到端集成测试 |

## 2. 数据模型

### 2.1 原始记录 `ledger.Record`

| 字段 | 类型 | 说明 |
|---|---|---|
| `ID` | string | 记录唯一标识，幂等键 |
| `Timestamp` | int64 | 发生时间（非负），决定规范化顺序 |
| `From` / `To` | string | 转出/转入账户，均非空且不相同 |
| `Amount` | string | 以字符串承载，摄取时严格解析为 int64（最小货币单位） |

金额故意用字符串输入，使"类型非法"成为一类必须处理、且必须被**显式拒绝**的
输入，而不是在反序列化阶段被悄悄丢弃。

### 2.2 规范化交易 `ledger.Tx`

通过校验后的不可变结构：`{ID, Timestamp, From, To, Amount int64}`。
账本对外只提供按 `(Timestamp, ID)` 升序排列的快照。

### 2.3 拒绝原因（可区分、可统计）

| 原因常量 | 值 | 触发条件 |
|---|---|---|
| `ReasonMissingID` | `missing_id` | ID 为空（无法做幂等判断，优先级最高） |
| `ReasonMissingFrom` | `missing_from` | 转出账户为空 |
| `ReasonMissingTo` | `missing_to` | 转入账户为空 |
| `ReasonSelfTransfer` | `self_transfer` | From == To |
| `ReasonBadTimestamp` | `invalid_timestamp` | 时间戳为负 |
| `ReasonBadAmount` | `invalid_amount` | 金额为空 / 非整数 / 超出 int64 / 非正数 |
| `ReasonDuplicate` | `duplicate_id` | 已接受过的 ID，或同一批次内重复出现 |

校验按**固定优先级**执行，同一条输入无论投递多少次、与什么数据混批，拒绝原因
永远相同。

## 3. 摄取语义（ledger）

- **增量摄取**：`Ingest([]Record) IngestResult` 可多次调用，返回本次新接受与被
  拒绝的记录副本。
- **乱序透明化**：最终顺序只由 `(Timestamp, ID)` 决定。同一批数据无论以何种
  到达顺序、分多少批投递，`Txs()` 快照完全相同（由
  `TestIngestOutOfOrderDeterministic` 对照验证）。
- **幂等去重**：已接受的 ID 再次出现一律 `duplicate_id` 拒绝；同一批次内重复
  同样拒绝。重复处理不修改任何已提交状态。被拒绝的坏记录重投仍按同一原因
  拒绝，永远不会"侥幸进入"账本。
- **不静默忽略**：任何未被接受的记录都带明确原因与人类可读 `Detail` 出现在
  `IngestResult.Rejected` 中。
- 并发安全：`sync.RWMutex` 保护内部 map 与切片；`Txs()` 返回深拷贝快照。

## 4. 交易图与风险模式（graph）

`graph.Build([]Edge)` 内部再次按 `(Time, TxID)` 排序，因此不依赖调用方的输入
顺序；账户列表、邻接表、输出模式均确定。

### 4.1 模式定义与判定依据

| 模式 | 判定规则 | 依据字段（可核对） |
|---|---|---|
| 汇聚 `fan_in` | 某账户收到 ≥ `FanInSources` 个**不同来源**的入账 | 来源账户集合、入账 TxID 集合、入账总金额 |
| 拆分 `fan_out` | 某账户向 ≥ `FanOutTargets` 个**不同去向**转出 | 去向集合、出账 TxID 集合、出账总金额 |
| 环路 `cycle` | 有向图中存在闭合环 | 环上账户序列、环上各跳 TxID、环上金额合计 |
| 异常集中 `hotspot` | 流经总额 ≥ 阈值，或交易笔数 ≥ 3 且 ≥ 3× 全体中位数 | 触发判据文字、涉及 TxID、流经总额 |

环路检测的确定性与安全性：

- DFS + 当前路径栈；一个环只有在"环上字典序最小账户 == DFS 根账户"时才被
  报告，因此**每个不同环恰好出现一次**，与起始遍历点无关。
- 深度受 `MaxCycleDepth` 限制（默认 32，硬封顶 256）；超深链路直接终止搜索，
  2000 / 5000 跳无环链的测试验证了不会挂起、不会误判。
- 每类模式输出数量有安全上限（1024），防止超大输入放大输出。

## 5. 评分与规则版本（risk）

### 5.1 评分

账户得分 = 命中模式权重之和（封顶 100）。默认 v1 权重：

| 模式 | 权重 |
|---|---|
| fan_in | 20 |
| fan_out | 20 |
| cycle | 40 |
| hotspot | 30 |

等级（纯函数 `LevelForScore`）：`[0,40) low`，`[40,70) medium`，`[70,100] high`。
报告中的账户按账户名排序、模式按 (类别, 依据文本) 排序，全部依据随结论返回。

### 5.2 确定性保证

`Report` **不含时间戳、随机数或 map 迭代产物**，并携带：

- `RuleVersion`：规则版本号与名称；
- `InputHash`：对规范化交易序列的 SHA-256 前 8 字节十六进制指纹。

恒等关系：**相同规则版本 + 相同 InputHash ⇒ 逐字节相同的 Report**。
`analyze` 是纯函数，`Analyze` 只在读锁内取规则快照后调用它。

### 5.3 规则更新与回放

- `UpdateRules(RuleSet)` 原子切换当前规则，并把新版本登记进历史（同版本号
  重复提交不会重复登记）。
- `Replay(RuleSet, Ledger)` 用**指定版本规则**对同一份输入重算，不改变当前
  生效规则。旧结论因此可随时逐字复现；规则变化前后对同一 `InputHash` 的
  两份结论可直接对比（见 `TestRuleUpdateAndReplay`）。
- `History()` 返回全部曾生效版本。

## 6. 交易模拟（sim）

- `New(ledger)` 对账本做一次快照，由已确认交易确定性推导初始余额（流入为正、
  流出为负；出资方余额可为负，表示净出金）。
- `Transfer` 校验：金额为正、双方账户已存在、余额充足；失败原因分别为
  `invalid_amount` / `unknown_account` / `insufficient_funds`，**失败不改变状态**。
- `Chain(steps, maxDepth)` 原子执行一条资金链：先在临时视图上校验全部跳，
  全部通过才提交，任一跳失败则整链回滚。
- 安全上限：链长硬性封顶 `MaxChainDepth = 100`，超限返回 `depth_exceeded`；
  环是"有限跳列表"中天然允许的结构（如 c→a→b→c），不会导致无限循环。
- 模拟交易获得确定性 ID（`sim-000001`…）与排在真实交易之后的时间戳；
  `Graph()` 导出"真实 + 模拟"图供风险引擎分析；`Reset()` 清空模拟。

## 7. 并发与一致性保证汇总

1. `Ledger`、`Engine`、`Simulator` 均为互斥锁保护的线程安全类型。
2. 所有对外查询返回深拷贝/新分配的快照，调用方持有后不受后续写入影响。
3. 分析过程是无副作用的纯函数式计算；并发分析同一份数据结论逐字节一致
   （`-race` 下的 `TestConcurrentAnalyzeDeterministic` 用 40 读 + 8 并发重复
   摄取验证）。
4. 规则切换对读者是原子的：一次分析自始至终使用同一版本规则。
5. 所有排序键显式给定，输出路径上不存在 map 随机遍历。

## 8. 本地验证方法

```bash
# 编译与静态检查
go build ./...
go vet ./...
gofmt -l .   # 应无输出

# 全部单测（含输入、判定依据、结论日志）
go test ./... -v

# 竞态检测 + 确定性（多跑几遍结果必须相同）
go test ./... -race -count=3
```

测试矩阵：

| 关注点 | 测试 |
|---|---|
| 拒绝原因可区分 | `ledger.TestIngestValidationReasons` |
| 重复投递幂等 | `ledger.TestIngestDuplicateIdempotent` |
| 乱序最终状态一致 | `ledger.TestIngestOutOfOrderDeterministic` |
| 超大/越界金额 | `ledger.TestIngestLargeAmountSafe` |
| 四类模式识别与依据 | `graph.TestDetectFanInFanOut/TestDetectCyclesDeterministic/TestHotspots` |
| 超深链路不崩溃不挂起 | `graph.TestDeepChainDoesNotHangOrDetect`（2000 跳） |
| 评分可解释 | `risk.TestAnalyzeExplainable` |
| 并发结论一致 | `risk.TestConcurrentAnalyzeDeterministic`（-race） |
| 规则更新与回放 | `risk.TestRuleUpdateAndReplay` |
| 模拟校验/原子性/深度 | `sim.TestTransferValidation/TestChainAtomicityAndDepthGuard` |
| 模拟图 + 规则回放 | `sim.TestSimulatedGraphRuleReplay` |
| 全链路 + 5000 跳超大输入 | `e2e.TestEndToEnd` |

单测日志统一打印三项内容：**输入摘要 → 判定依据（模式、TxID、数值）→
最终结论（分数、等级、版本、输入指纹）**。

## 9. 关键边界与取舍

- 金额采用 int64 最小单位，不引入浮点；越界字符串按非法拒绝。
- 乱序采用"最终一致的全序快照"模型：摄取立即接受并可查，查询永远呈现
  `(Timestamp, ID)` 全序，无需调用方等待。
- 环路以账户环去重（平行边不重复报告），证据取每对账户之间最早的一条边。
- 规则"更新"是不可变值的版本发布，而不是原地改配置，这是回放与可追溯性
  的基础。
