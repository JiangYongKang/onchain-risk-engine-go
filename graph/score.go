package graph

import "sort"

// Weights 是各模式在评分中的权重（来自规则集）。
type Weights struct {
	FanIn         int
	FanOut        int
	Cycle         int
	Concentration int
}

// Report 是一次风险分析的完整结论。
type Report struct {
	Score    int       // 综合风险分
	Level    string    // low / medium / high / critical
	Findings []Finding // 判定依据，可逐条核对
	Accounts []string  // 涉及的高风险账户，排序输出
}

// weightOf 返回某类模式的权重。
func weightOf(k PatternKind, w Weights) int {
	switch k {
	case PatternFanIn:
		return w.FanIn
	case PatternFanOut:
		return w.FanOut
	case PatternCycle:
		return w.Cycle
	case PatternConcentration:
		return w.Concentration
	}
	return 0
}

// Level 把分数映射为等级，阈值固定以保证可核对。
func Level(score int) string {
	switch {
	case score >= 100:
		return "critical"
	case score >= 60:
		return "high"
	case score >= 30:
		return "medium"
	default:
		return "low"
	}
}

// Score 根据发现列表与权重计算确定性评分报告。
// 评分公式：score = Σ weight(kind)（每条 finding 计一次），
// 公式简单可手工核对；finding 顺序由 Analyze 保证确定。
func Score(findings []Finding, w Weights) Report {
	score := 0
	accSet := make(map[string]struct{})
	for _, f := range findings {
		score += weightOf(f.Kind, w)
		for _, a := range f.Accounts {
			accSet[a] = struct{}{}
		}
	}
	accts := make([]string, 0, len(accSet))
	for a := range accSet {
		accts = append(accts, a)
	}
	sort.Strings(accts)
	return Report{
		Score:    score,
		Level:    Level(score),
		Findings: findings,
		Accounts: accts,
	}
}
