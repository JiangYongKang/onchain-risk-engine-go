package risk

import "github.com/highcumontoa/onchain-risk-engine-go/internal/graph"

// RuleVersion 标识一套规则（变化即产生新版本，保证可回放/可追溯）。
type RuleVersion struct {
	Version int
	Name    string
}

// RuleSet 是风险规则的不可变配置集合。
// RuleSet 以值方式传递与保存，更新规则等价于发布一个新版本而非原地修改。
type RuleSet struct {
	Version       RuleVersion
	FanInSources  int   // 汇聚：最少不同来源数
	FanOutTargets int   // 拆分：最少不同去向数
	MaxCycleDepth int   // 环路搜索最大深度（0 用默认值，内部再封顶）
	HotspotAmount int64 // 异常集中金额阈值（<=0 表示关闭金额判据）
	// 每种模式的扣分权重：账户得分 = 命中模式权重之和（封顶 100）。
	Weights map[graph.PatternKind]int
}

// defaultWeights 是首版默认权重。
func defaultWeights() map[graph.PatternKind]int {
	return map[graph.PatternKind]int{
		graph.PatternFanIn:   20,
		graph.PatternFanOut:  20,
		graph.PatternCycle:   40,
		graph.PatternHotspot: 30,
	}
}

// DefaultRuleSet 返回首版默认规则（v1）。
func DefaultRuleSet() RuleSet {
	return RuleSet{
		Version:       RuleVersion{Version: 1, Name: "default-v1"},
		FanInSources:  3,
		FanOutTargets: 3,
		MaxCycleDepth: 32,
		HotspotAmount: 1_000_000,
		Weights:       defaultWeights(),
	}
}

// ScoreLevel 风险等级。
type ScoreLevel string

const (
	LevelLow    ScoreLevel = "low"
	LevelMedium ScoreLevel = "medium"
	LevelHigh   ScoreLevel = "high"
)

// LevelForScore 将分值映射为等级（纯函数）：
//
//	[0,40) low；[40,70) medium；[70,100] high。
func LevelForScore(score int) ScoreLevel {
	switch {
	case score >= 70:
		return LevelHigh
	case score >= 40:
		return LevelMedium
	default:
		return LevelLow
	}
}
