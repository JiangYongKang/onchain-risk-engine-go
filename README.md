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

## 快速开始

```go
l := ledger.New()
l.Ingest([]ledger.Record{ /* 可乱序、可分批、可含重复/坏记录 */ })

eng := risk.NewEngine(risk.DefaultRuleSet())
report := eng.Analyze(l) // 并发安全、确定；report.Accounts[i].Patterns 即依据

eng.UpdateRules(newRules)        // 发布新规则
old := eng.Replay(risk.DefaultRuleSet(), l) // 同输入复现旧结论
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
