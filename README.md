# onchain-risk-engine-go

本地交易图风险分析与交易模拟引擎：用本地账本数据模拟链上资金流，在重复、
乱序和异常输入下输出**确定、可解释、可回放**的风险结论。纯 Go 标准库实现，
无外部依赖。

## 能力一览

- **增量摄取与规范化**（`internal/ledger`）
  - 乱序到达 → 按 `(时间, ID)` 全序重排，最终状态与到达顺序无关
  - 重复投递 → `duplicate_id` 拒绝，重复处理不改变状态（幂等）
  - 字段缺失 / 类型非法 / 自转 / 负时间戳 → 按 7 种原因分类拒绝，绝不静默忽略
  - 拒绝原因稳定：同一条记录无论怎么分批、按什么顺序、和什么数据混批，
    拒绝原因永远相同，可按原因稳定分类统计
- **交易图风险分析**（`internal/graph` + `internal/risk`）
  - 识别资金汇聚、资金拆分、有向环路、异常集中四类模式
  - 每个结论附来源/去向账户、交易 ID、金额等可核对依据
  - 评分 → 风险等级；报告携带规则版本与输入指纹（SHA-256）
  - 同一规则 + 同一输入 ⇒ 逐字节一致的报告，并发分析安全
- **下钻溯源**（`internal/graph`）
  - 对被标记账户或指定交易，按发生顺序还原资金流入/流出链路（途经账户、
    交易、金额、跳数）
  - 几万节点级超长链、大环也能完整正确还原（迭代式 BFS，不递归、不漏跳）
  - 触及安全上限时显式标记 `Truncated` 并回显生效上限，绝不伪装成完整链路
- **跨版本结论对比**（`internal/risk`）
  - 两套规则版本在同一份输入上的差异：等级变化、多/少命中的模式及依据
  - 纯函数 + 显式排序键，先算哪个版本、重复多少遍结果都逐字节一致
- **增量重算**（`internal/risk`）
  - 数据分批持续喂入，增量结论与全量重算必然逐字节一致（共用同一纯函数）
  - 并发摄取/分析/模拟安全，结论永不暴露算到一半的中间状态
- **资金模拟与规则回放**（`internal/sim` + `internal/risk`）
  - 单笔/链式资金变动模拟，余额不足、未知账户、非法金额明确拒绝
  - 链式模拟原子提交；链长硬封顶，超深链/环路不会崩溃或无限等待
  - 规则以不可变版本发布，`Replay` 可随时逐字复现旧版本结论，历史可追溯

## 快速开始

```go
l := ledger.New()
l.Ingest([]ledger.Record{ /* 可乱序、可分批、可含重复/坏记录 */ })

eng := risk.NewEngine(risk.DefaultRuleSet())
report := eng.Analyze(l) // 并发安全、确定；report.Accounts[i].Patterns 即依据

eng.UpdateRules(newRules)        // 发布新规则
old := eng.Replay(risk.DefaultRuleSet(), l) // 同输入复现旧结论

// 下钻溯源：还原被标记账户的资金来龙去脉
g := sim.New(l).Graph()
tr := g.TraceAccount("some-account", graph.TraceOptions{})
// tr.Inbound / tr.Outbound 按时间排序；tr.Truncated 为 true 时表示触限截断

// 跨版本对比：同一份输入上两套规则差在哪
diff := eng.Compare(oldRules, newRules, l) // diff.Accounts 列出所有结论变化

// 增量重算：分批喂数据，结论始终等于全量重算
inc := risk.NewIncremental(risk.DefaultRuleSet())
inc.AddFromLedger(l)      // 每投一批新数据后调用
rep := inc.Report()       // 与 eng.Analyze(l) 逐字节一致
```

## 本地验证

```bash
go build ./...
go vet ./...
go test ./... -v            # 日志含：输入、判定依据、最终结论
go test ./... -race -count=3
```

## 文档

- [docs/design.md](docs/design.md) — 数据模型、判定规则、评分权重、并发与
  一致性保证、测试矩阵与本地验证方法
