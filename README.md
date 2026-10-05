# onchain-risk-engine-go

本地交易图风险分析与交易模拟引擎：用本地账本数据模拟链上资金流，在重复、
乱序和异常输入下输出**确定、可解释、可回放**的风险结论。纯 Go 标准库实现，
无外部依赖。

## 能力一览

- **增量摄取与规范化**（`internal/ledger`）
  - 乱序到达 → 按 `(时间, ID)` 全序重排，最终状态与到达顺序无关
  - 重复投递 → `duplicate_id` 拒绝，重复处理不改变状态（幂等）
  - 字段缺失 / 类型非法 / 自转 / 负时间戳 → 按 7 种原因分类拒绝，绝不静默忽略
- **交易图风险分析**（`internal/graph` + `internal/risk`）
  - 识别资金汇聚、资金拆分、有向环路、异常集中四类模式
  - 每个结论附来源/去向账户、交易 ID、金额等可核对依据
  - 评分 → 风险等级；报告携带规则版本与输入指纹（SHA-256）
  - 同一规则 + 同一输入 ⇒ 逐字节一致的报告，并发分析安全
- **资金模拟与规则回放**（`internal/sim` + `internal/risk`）
  - 单笔/链式资金变动模拟，余额不足、未知账户、非法金额明确拒绝
  - 链式模拟原子提交；链长硬封顶，超深链/环路不会崩溃或无限等待
  - 规则以不可变版本发布，`Replay` 可随时逐字复现旧版本结论，历史可追溯
- **下钻溯源**（`internal/trace`）
  - 对被标记账户或指定交易，按发生先后还原“资金怎么流进来、流向哪里”：
    经过的账户、交易、上下游路径与覆盖子图内的资金环（含依据交易与金额）
  - 覆盖数万节点的真实长链与大环：线性算法（Kosaraju SCC + Johnson 环枚举），
    不漏段、不卡住；触发安全上限时显式返回 `Truncated/LimitName/Frontier`，
    不会让你把截断结果误当成完整链路
- **跨版本结论对比**（`risk.CompareVersions`）
  - 同一份输入上两个规则版本的差异：风险等级/分数变化、多命中或少命中的模式
    及对应依据；纯函数，先算谁、跑多少遍结果逐字节一致
- **增量重算**（`internal/incremental`）
  - 按批持续摄取，任意时刻结论与“当前全量数据一次性重算”逐字节一致
  - 同一条记录无论怎么分批、什么顺序、与哪些数据混批，拒绝原因恒定
  - 边摄取边分析并发安全，读者只看到某次摄取前/后的完整结论，无半状态

## 快速开始

```go
l := ledger.New()
l.Ingest([]ledger.Record{ /* 可乱序、可分批、可含重复/坏记录 */ })

eng := risk.NewEngine(risk.DefaultRuleSet())
report := eng.Analyze(l) // 并发安全、确定；report.Accounts[i].Patterns 即依据

eng.UpdateRules(newRules)        // 发布新规则
old := eng.Replay(risk.DefaultRuleSet(), l) // 同输入复现旧结论

// 下钻溯源：被标记账户 x 的资金来源与去向（账户或指定交易均可）
r := trace.Trace(l.Txs(), trace.Target{Kind: trace.TargetAccount, Account: "x"}, trace.Limits{})
r.Upstream.Hops    // 钱怎么流进来（按发生先后）
r.Downstream.Hops  // 又流向哪里
r.Cycles           // 覆盖子图内的资金环；截断时 r.Upstream.Truncated 等显式标记

// 跨版本对比：同一输入上新老规则结论差异（确定、可复现）
diff := risk.CompareVersions(old, eng.Analyze(l))
// diff.Changed[i]：等级/分数变化、AddedPatterns/RemovedPatterns 及依据

// 增量重算：分批持续摄取，结论始终等于全量重算
acc := incremental.New()
acc.Ingest(batch1); acc.Ingest(batch2)
rep := acc.Analyze(risk.DefaultRuleSet()) // == 全量数据一次性重算
```

## 本地验证

```bash
go build ./...
go vet ./...
gofmt -l .
go test ./... -v            # 日志含：输入、判定依据、最终结论
go test ./... -race -count=3
```

新能力的重点边界用例（均已在常规 `go test ./...` 中）：

- 溯源：`trace.TestTraceVeryLongChain`（2 万节点长链，完整不漏）、
  `trace.TestTraceLargeRing`（2 万节点大环，恰好 1 个环）、
  `trace.TestTraceMultipleCyclesCorrect`（带弦图每环恰好一次）、
  `trace.TestTraceTruncationExplicit`（截断显式标记）。
- 对比：`risk.TestCompareVersionsDeterministic`（先算谁/跑多少遍都一致）。
- 增量：`incremental.TestIncrementalEqualsFullRecompute`、
  `...RandomPartitionOrder`（增量=全量逐字节）、
  `...ConcurrentNoHalfState`（-race 下并发摄取+分析无半状态）。
- 混批原因：`ledger.TestRejectReasonStableAcrossBatches`、
  `...SameIDConflictReplaced`。

## 文档

- [docs/design.md](docs/design.md) — 数据模型、判定规则、评分权重、并发与
  一致性保证、测试矩阵与本地验证方法
