package risk

import (
	"sort"
	"strings"

	"github.com/highcumontoa/onchain-risk-engine-go/internal/graph"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/ledger"
)

// patternKindOrder 是模式类别的固定排序键（与报告内依据排序一致）。
var patternKindOrder = []graph.PatternKind{
	graph.PatternFanIn, graph.PatternFanOut,
	graph.PatternCycle, graph.PatternHotspot,
}

// patternKey 是模式的稳定身份键：同一份输入、同一套规则下，
// 同一个模式必然产生同一个键；不同模式不会碰撞。
func patternKey(p graph.Pattern) string {
	return string(p.Kind) + "\x1f" + p.Detail +
		"\x1f" + strings.Join(p.Accounts, "\x1e") +
		"\x1f" + strings.Join(p.TxIDs, "\x1e")
}

func sortPatterns(ps []graph.Pattern) {
	sort.SliceStable(ps, func(i, j int) bool {
		ri, rj := rankOf(patternKindOrder, ps[i].Kind), rankOf(patternKindOrder, ps[j].Kind)
		if ri != rj {
			return ri < rj
		}
		return patternKey(ps[i]) < patternKey(ps[j])
	})
}

// AccountDiff 描述一个账户在两个规则版本下的结论差异。
type AccountDiff struct {
	Account         string
	OldScore        int
	NewScore        int
	OldLevel        ScoreLevel
	NewLevel        ScoreLevel
	LevelChanged    bool
	PatternsAdded   []graph.Pattern // 新规则多命中的模式（含判定依据）
	PatternsRemoved []graph.Pattern // 新规则不再命中的模式（含判定依据）
}

// ReportDiff 是两份报告（同一份输入、两个规则版本）的确定性差异。
//
// 确定性保证：DiffReports 是纯函数，所有输出列表排序键显式给定；
// 同样的两个版本 + 同样的输入，无论先算哪个版本、重复多少遍，
// 结果逐字节一致。
type ReportDiff struct {
	VersionA   RuleVersion
	VersionB   RuleVersion
	InputHashA string
	InputHashB string
	SameInput  bool          // 两份报告是否基于同一份输入（InputHash 相同）
	Accounts   []AccountDiff // 仅含结论有变化的账户，按账户名排序
}

// DiffReports 对比两份报告（纯函数）。约定 a 为旧版本、b 为新版本。
func DiffReports(a, b Report) ReportDiff {
	byA := map[string]AccountRisk{}
	for _, r := range a.Accounts {
		byA[r.Account] = r
	}
	byB := map[string]AccountRisk{}
	for _, r := range b.Accounts {
		byB[r.Account] = r
	}

	acctSet := map[string]struct{}{}
	for acct := range byA {
		acctSet[acct] = struct{}{}
	}
	for acct := range byB {
		acctSet[acct] = struct{}{}
	}
	accounts := make([]string, 0, len(acctSet))
	for acct := range acctSet {
		accounts = append(accounts, acct)
	}
	sort.Strings(accounts)

	diff := ReportDiff{
		VersionA:   a.RuleVersion,
		VersionB:   b.RuleVersion,
		InputHashA: a.InputHash,
		InputHashB: b.InputHash,
		SameInput:  a.InputHash == b.InputHash,
	}
	for _, acct := range accounts {
		ra, oka := byA[acct]
		rb, okb := byB[acct]

		// 模式按身份键做多重集差集：A 有 B 无 = removed，B 有 A 无 = added。
		countA := map[string]int{}
		repA := map[string]graph.Pattern{}
		for _, p := range ra.Patterns {
			k := patternKey(p)
			countA[k]++
			repA[k] = p
		}
		countB := map[string]int{}
		repB := map[string]graph.Pattern{}
		for _, p := range rb.Patterns {
			k := patternKey(p)
			countB[k]++
			repB[k] = p
		}
		var added, removed []graph.Pattern
		for k, p := range repB {
			for i := 0; i < countB[k]-countA[k]; i++ {
				added = append(added, p)
			}
		}
		for k, p := range repA {
			for i := 0; i < countA[k]-countB[k]; i++ {
				removed = append(removed, p)
			}
		}
		sortPatterns(added)
		sortPatterns(removed)

		levelChanged := oka != okb || ra.Level != rb.Level
		if !levelChanged && ra.Score == rb.Score && len(added) == 0 && len(removed) == 0 {
			continue // 结论完全一致，不进入差异列表
		}
		diff.Accounts = append(diff.Accounts, AccountDiff{
			Account:         acct,
			OldScore:        ra.Score,
			NewScore:        rb.Score,
			OldLevel:        ra.Level,
			NewLevel:        rb.Level,
			LevelChanged:    levelChanged,
			PatternsAdded:   added,
			PatternsRemoved: removed,
		})
	}
	return diff
}

// Compare 用两个规则版本对同一份账本快照各算一次并给出差异。
// 两个版本共享同一次 Txs() 快照，因此 SameInput 恒为 true；
// 计算顺序不影响结果（analyze 是纯函数）。
func (e *Engine) Compare(a, b RuleSet, l *ledger.Ledger) ReportDiff {
	txs := l.Txs() // 同一份输入快照，两个版本看到的是同一份数据
	ra := analyze(normalize(a), txs)
	rb := analyze(normalize(b), txs)
	return DiffReports(ra, rb)
}
