// Package rules 管理风险规则版本、分析与回放。
package rules

import (
	"github.com/highcumontoa/onchain-risk-engine-go/graph"
	"github.com/highcumontoa/onchain-risk-engine-go/ledger"
)

// RuleSet 是一个版本化的风险规则集。
type RuleSet struct {
	Version    int
	Thresholds graph.Thresholds
	Weights    graph.Weights
}

// Default 返回内置默认规则（版本 1）。
func Default() RuleSet {
	return RuleSet{
		Version: 1,
		Thresholds: graph.Thresholds{
			FanInMinSources:    3,
			FanOutMinTargets:   3,
			ConcentrationShare: 50,
			MinCycleVolume:     1,
			MaxNodes:           100000,
		},
		Weights: graph.Weights{
			FanIn:         20,
			FanOut:        20,
			Cycle:         40,
			Concentration: 30,
		},
	}
}

// Engine 在指定规则下对账本快照执行分析。
type Engine struct {
	rules RuleSet
}

// NewEngine 创建分析引擎。
func NewEngine(r RuleSet) *Engine {
	return &Engine{rules: r}
}

// Rules 返回当前规则集。
func (e *Engine) Rules() RuleSet {
	return e.rules
}

// Analyze 对账本快照产出风险报告。它是纯函数：
// 不读写任何共享状态，因此可安全并发调用且结果确定。
func (e *Engine) Analyze(tx []ledger.Transfer) graph.Report {
	g := graph.Build(tx)
	findings := graph.Analyze(g, e.rules.Thresholds)
	return graph.Score(findings, e.rules.Weights)
}
