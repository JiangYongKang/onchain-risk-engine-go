# 本地交易图风险分析与交易模拟 — 设计说明

## 1. 目标与范围

用本地账本数据模拟链上资金流，在**重复投递、乱序到达、字段缺失、类型非法**等
非理想输入下给出**稳定、确定、可解释**的风险结论，并支持资金模拟与风险规则的
版本化更新、回放。全部能力为纯本地实现，无外部依赖、无网络 IO。

包结构：

| 包 | 职责 |
|---|---|
| `internal/ledger` | 本地账本的增量摄取、规范化、去重与拒绝原因分类（含同 ID 确定性冲突裁决） |
| `internal/graph` | 由交易构建只读有向图，识别汇聚/拆分/环路/异常集中 |
| `internal/risk` | 规则集版本化、确定性评分、并发分析、规则回放、跨版本结论对比 |
| `internal/sim` | 账户余额推导、单笔/链式资金模拟、超深防护 |
| `internal/trace` | 账户/交易下钻溯源：上下游资金路径还原与覆盖子图环枚举 |
| `internal/incremental` | 按批增量维护交易视图，结论与全量重算逐字节一致 |
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
| `ReasonDuplicate` | `duplicate_id` | 同 ID 内容完全相同的重复投递；或同 ID 但内容全序上更大、被确定性淘汰 |

校验按**固定优先级**执行：结构性非法（缺字段/自转/负时间戳/非法金额）**先于**
重复判定，因此坏记录即使与某有效记录同 ID、且有效记录先到，也永远得到它的结构
原因，绝不会被顶成 `duplicate_id`；同一条记录无论投递多少次、与什么数据混批，
拒绝原因永远相同（`ledger.Classify` 是这一结论的纯函数视角，供核对）。

### 2.4 同 ID 冲突的确定性裁决

当同一 ID 出现**内容不同**的有效记录时，谁生效只取决于记录内容的全序
`(Timestamp, From, To, Amount)`：内容更小者生效，另一条以 `duplicate_id` 拒绝，
并在 `IngestResult.Replaced` 中明确报告被移出的旧交易。于是：

- 最终账本状态只由“记录集合”决定，与到达顺序、分批方式无关；
- 增量视图在冲突发生时能纠正先前结论，始终与一次性全量归并一致。

## 3. 摄取语义（ledger）

- **增量摄取**：`Ingest([]Record) IngestResult` 可多次调用，返回本次新接受与被
  拒绝的记录副本。
- **乱序透明化**：最终顺序只由 `(Timestamp, ID)` 决定。同一批数据无论以何种
  到达顺序、分多少批投递，`Txs()` 快照完全相同（由
  `TestIngestOutOfOrderDeterministic` 对照验证）。
- **幂等去重**：内容相同的记录重投一律 `duplicate_id` 拒绝（批内/批间同规则）。
  重复处理不修改任何已提交状态。被拒绝的坏记录重投仍按同一结构原因拒绝，永远
  不会“侥幸进入”账本。
- **同 ID 冲突**：内容不同的有效记录按内容全序定胜负（见 2.4），与顺序无关，
  被淘汰者在 `Replaced` 中报告。
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

### 5.4 跨版本结论对比 `risk.CompareVersions(old, new)`

调规则后想看“同一份数据上新老结论哪里不一样”，用 `CompareVersions` 得到
`Diff`：

- `Changed`：按账户名排序的差异列表 `AccountDiff`，含旧/新分数与等级、
  `LevelChanged`，以及 `AddedPatterns`（新版本多命中）与
  `RemovedPatterns`（新版本少命中）；每个模式引用都带类别、账户集合、
  交易集合等**判定依据**。
- `Unchanged`：等级与命中模式都未变化的账户。

匹配键是模式的稳定签名 `(Kind, 排序账户集, 排序交易集)`，**刻意不纳入
Detail 文案与 Value**——文案微调但依据相同不会被误报为差异。

确定性：`CompareVersions` 是两份报告内容的纯函数，方向固定为 old→new，
集合做对称差、账户与模式都显式排序，输出路径无 map 随机遍历。因此先算哪个
版本、是否并发、运行多少遍，`Diff` 逐字节一致；反向对比（new→old）恰为
正向镜像。由 `risk.TestCompareVersionsDeterministic` 验证。

## 5A. 下钻溯源 `internal/trace`

针对“某账户有风险”这个结论需要向人解释资金路径的场景，`trace.Trace` 对
**某个被标记账户**或**指定的一笔交易**还原资金怎么流进来、又流向哪里。

### 5A.1 锚点与方向

- `Target{Kind: TargetAccount, Account}`：以账户为锚点，沿入边做上游 BFS、
  沿出边做下游 BFS。
- `Target{Kind: TargetTx, TxID}`：以交易为锚点，上游从该交易转出方追溯
  （钱从哪来），下游从收款方继续展开（又流向哪），并在 `AnchorTx` 返回该
  交易本身。锚点不存在时 `Found=false` 并在 `Notes` 说明。

每个方向的 `DirectionResult` 给出：

- `Reachable`：BFS 层序访问到的账户（确定顺序）；
- `Hops`：覆盖子图内的**全部**边，按 `(Time, TxID)` 全序排列（含回边，
  因此环形回流不会漏），每跳带交易 ID、金额、时间、距锚点跳数；
- 截断信息：`Truncated`、`LimitName`（`nodes`/`hops`）、未能继续展开的
  `Frontier` 账户。

### 5A.2 长链与大环的可扩展性

- 上/下游各是一次 O(V+E) BFS；2 万节点无环长链在约 0.1s 内完整还原，
  端点可达即证明没有悄悄少算某一段。
- 环枚举分两步：先做 **Kosaraju SCC 分解**（线性、迭代实现避免深递归），
  平凡（无环）分量直接跳过，所以长链不会触发环枚举；再在每个非平凡 SCC 内
  用 **Johnson(1975) 算法**枚举简单环，每个环以其最小顶点为根**恰好一次**。
  2 万节点简单大环线性完成（约 0.1s），带弦图也能逐环不漏不重。

### 5A.3 上限与截断的显式契约

安全上限在 `Limits{MaxNodes, MaxHops}`（默认 200_000 / 500_000，足以覆盖
数万节点量级）；环输出另有硬上限 `maxCycles=100_000`。任何上限触发都**不会
静默截断**：对应方向置 `Truncated=true`、给出 `LimitName` 与 `Frontier`，
环超限时 `CyclesTruncated=true`，并在 `Notes` 用人类可读文字说明实际生效上限。
调用方可据此区分“完整链路”与“被截断的链路”，绝不会误判。

## 5B. 增量重算 `internal/incremental`

`incremental.Accumulator` 支持数据一批批持续增长：

- `Ingest([]Record)` 复用 `ledger` 的全部规范化、拒绝原因与同 ID 冲突规则，
  因此同一条记录无论怎么分批、什么顺序、与哪些数据混在一起，最终视图与
  拒绝原因都与“一次性全量摄取”完全相同。
- `Analyze(rules)` **不另写一套增量打分逻辑**，而是把当前边集交给与全量
  分析相同的纯函数 `risk.AnalyzeTxs`。由于边集与全量一致、分析是纯函数，
  “增量结论 == 全量重算结论”是构造性保证，不会因两套实现演化而漂移。
- 视图在写锁内一次性重建，读者持读锁取快照；并发摄取 + 分析时，读者只会
  看到某次摄取之前或之后的完整状态，不会看到边集更新一半的中间结论。
- 重复喂入同一批幂等（视图与结论不变）。

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

1. `Ledger`、`Engine`、`Simulator`、`Accumulator` 均为锁保护的线程安全类型。
2. 所有对外查询返回深拷贝/新分配的快照，调用方持有后不受后续写入影响。
3. 分析过程是无副作用的纯函数式计算；并发分析同一份数据结论逐字节一致
   （`-race` 下的 `risk.TestConcurrentAnalyzeDeterministic` 用 40 读 + 8 并发重复
   摄取验证；`incremental.TestIncrementalConcurrentNoHalfState` 用 20 并发写 +
   8 持续读验证无半状态）。
4. 规则切换对读者是原子的：一次分析自始至终使用同一版本规则。
5. 所有排序键显式给定，输出路径上不存在 map 随机遍历。
6. 溯源（`trace.Trace`）是只读纯计算：BFS 邻接按时间序展开、SCC/环枚举确定，
   同输入重复运行或改变输入边序，结论逐字节一致。
7. 跨版本对比（`risk.CompareVersions`）是两份报告的纯函数，与计算先后无关。
8. 增量视图与全量共用同一分析纯函数，边集一致即结论一致（构造性保证）。

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
| 混批/换序拒绝原因恒定 | `ledger.TestRejectReasonStableAcrossBatches`（30 随机分批） |
| 同 ID 冲突定胜负与全量一致 | `ledger.TestSameIDConflictReplaced` |
| 四类模式识别与依据 | `graph.TestDetectFanInFanOut/TestDetectCyclesDeterministic/TestHotspots` |
| 超深链路不崩溃不挂起 | `graph.TestDeepChainDoesNotHangOrDetect`（2000 跳） |
| 评分可解释 | `risk.TestAnalyzeExplainable` |
| 并发结论一致 | `risk.TestConcurrentAnalyzeDeterministic`（-race） |
| 规则更新与回放 | `risk.TestRuleUpdateAndReplay` |
| 跨版本差异与依据 | `risk.TestCompareVersionsDifferences` |
| 跨版本对比确定性 | `risk.TestCompareVersionsDeterministic`（先算谁/多遍一致、反向镜像） |
| 相同版本对比无差异 | `risk.TestCompareVersionsIdentical` |
| 溯源小链路/交易锚点 | `trace.TestTraceAccountSmallPath/TestTraceTxAnchor` |
| 溯源超长链完整不卡 | `trace.TestTraceVeryLongChain`（2 万节点） |
| 溯源大环恰好一个 | `trace.TestTraceLargeRing`（2 万节点） |
| 溯源多环不漏不重 | `trace.TestTraceMultipleCyclesCorrect`（带弦图） |
| 溯源截断显式标记 | `trace.TestTraceTruncationExplicit` |
| 溯源确定性/顺序无关 | `trace.TestTraceDeterministicAndOrderIndependent` |
| 增量=全量逐字节 | `incremental.TestIncrementalEqualsFullRecompute` |
| 随机分批/换序/冲突一致 | `incremental.TestIncrementalRandomPartitionOrder`（40 组） |
| 增量幂等 | `incremental.TestIncrementalIdempotent` |
| 并发摄取+分析无半状态 | `incremental.TestIncrementalConcurrentNoHalfState`（-race） |
| 模拟校验/原子性/深度 | `sim.TestTransferValidation/TestChainAtomicityAndDepthGuard` |
| 模拟图 + 规则回放 | `sim.TestSimulatedGraphRuleReplay` |
| 全链路 + 5000 跳超大输入 | `e2e.TestEndToEnd` |
| 新能力串联（溯源/对比/增量） | `e2e.TestNewCapabilitiesDrillDownDiffIncremental` |

单测日志统一打印三项内容：**输入摘要 → 判定依据（模式、TxID、数值）→
最终结论（分数、等级、版本、输入指纹）**。

## 9. 关键边界与取舍

- 金额采用 int64 最小单位，不引入浮点；越界字符串按非法拒绝。
- 乱序采用"最终一致的全序快照"模型：摄取立即接受并可查，查询永远呈现
  `(Timestamp, ID)` 全序，无需调用方等待。
- 环路以账户环去重（平行边不重复报告），证据取每对账户之间最早的一条边。
- 规则"更新"是不可变值的版本发布，而不是原地改配置，这是回放与可追溯性
  的基础。
- **同 ID 冲突语义变更的兼容范围**：旧版对"同 ID 不同内容"一律拒绝后来者
  （先到者保留），结论依赖到达顺序；新版改为内容全序定胜负。对"内容相同的
  重复投递"与"坏记录拒绝"两类既有行为完全兼容（原有全部用例仍通过）。
  若历史数据中确曾存在同 ID 不同内容的记录，新语义下最终留存者可能与旧版
  不同——这是为换取"顺序无关、增量=全量"的有意修正，且被替换记录会在
  `IngestResult.Replaced` 中显式列出。
- **既有风险报告环路检测的边界**：`graph.DetectCycles` 面向常见稀疏图，
  深度默认 32、硬封顶 256。在"环上存在大量平行边"这类极端稠密图上其朴素
  DFS 路径会组合放大；需要对大规模/密集图做环分析时应使用 `trace` 包的
  SCC+Johnson 线性算法（溯源结果不受该限制）。两者输出口径一致（每环以
  最小账户为根、证据取最早边）。
- **溯源截断而非静默少算**：默认上限覆盖数万节点量级；确需更大可显式调大
  `Limits`，但任何截断都会在结果中明确标注，调用方必须据 `Truncated` 判断
  完整性，不能假设返回即完整。
- **增量的"全量一致"口径**：指当前全部**已接受**记录的全序视图；被拒绝的
  记录从不属于视图。增量分析复用全量纯函数，保证口径单一、不漂移。
- **并发一致性边界**：锁保证单次 `Ingest`/`Analyze` 的原子快照语义，读者
  不会读到撕裂状态；但"某个具体批次何时对读者可见"的**线程间先后顺序**不做
  保证（取决于调度），最终收敛状态确定且一致。
