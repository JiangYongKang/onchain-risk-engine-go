package risk

import (
	"sort"
	"strings"

	"github.com/highcumontoa/onchain-risk-engine-go/internal/graph"
)

// PatternRef 以稳定签名引用一个模式命中，作为跨版本匹配依据。
type PatternRef struct {
	Kind      graph.PatternKind
	Signature string // (Kind, 排序账户集, 排序交易集) 的稳定签名
	Detail    string
	TxIDs     []string
	Accounts  []string
}

// AccountDiff 是同一账户在两个规则版本下的结论差异。
type AccountDiff struct {
	Account string
	// OldScore/NewScore 与等级。
	OldScore     int
	NewScore     int
	OldLevel     ScoreLevel
	NewLevel     ScoreLevel
	LevelChanged bool
	// AddedPatterns：新版本多命中的模式（按模式签名匹配）。
	AddedPatterns []PatternRef
	// RemovedPatterns：新版本少命中的模式。
	RemovedPatterns []PatternRef
}

// Changed 报告该账户是否存在任何实质差异（等级、分数或命中模式）。
func (d AccountDiff) Changed() bool {
	return d.LevelChanged || d.OldScore != d.NewScore ||
		len(d.AddedPatterns) != 0 || len(d.RemovedPatterns) != 0
}

// Diff 是两个规则版本在同一份输入上的完整结论差异。
// 结果只依赖 (oldReport, newReport) 的内容，与计算先后、运行次数、并发无关。
type Diff struct {
	OldVersion RuleVersion
	NewVersion RuleVersion
	InputHash  string
	Changed    []AccountDiff // 按账户名排序；只含有实际变化的账户
	Unchanged  []string      // 等级与命中模式均未变化的账户，按账户名排序
}

// signature 生成模式的跨版本稳定签名：类别 + 排序后的账户集与交易集。
// 有意不纳入 Detail 文案与 Value，使“文案微调但依据相同”不会被误报为差异。
func signature(p graph.Pattern) string {
	accts := append([]string(nil), p.Accounts...)
	sort.Strings(accts)
	txids := append([]string(nil), p.TxIDs...)
	sort.Strings(txids)
	return string(p.Kind) + "|" + strings.Join(accts, ",") + "|" + strings.Join(txids, ",")
}

func toRef(p graph.Pattern) PatternRef {
	return PatternRef{
		Kind: p.Kind, Signature: signature(p), Detail: p.Detail,
		TxIDs:    append([]string(nil), p.TxIDs...),
		Accounts: append([]string(nil), p.Accounts...),
	}
}

// CompareVersions 对比两份报告（应由两个规则版本对同一输入产生）。
//
// 确定性保证：Diff 是两份报告内容的纯函数。先算 old 还是先算 new、是否
// 并发、运行多少遍，输出逐字节一致——差异方向固定为 old -> new，模式集合
// 做对称差，账户与模式均显式排序，无 map 迭代产物。
func CompareVersions(old, new Report) Diff {
	d := Diff{
		OldVersion: old.RuleVersion, NewVersion: new.RuleVersion,
		InputHash: new.InputHash,
	}
	// 以账户为键索引两份报告。
	oldMap := map[string]AccountRisk{}
	newMap := map[string]AccountRisk{}
	allAccounts := map[string]struct{}{}
	for _, a := range old.Accounts {
		oldMap[a.Account] = a
		allAccounts[a.Account] = struct{}{}
	}
	for _, a := range new.Accounts {
		newMap[a.Account] = a
		allAccounts[a.Account] = struct{}{}
	}

	patternMap := func(ar AccountRisk) map[string]graph.Pattern {
		m := map[string]graph.Pattern{}
		for _, p := range ar.Patterns {
			// 同一账户下同一签名不可能重复（模式由图分析去重）；稳妥起见保留首个。
			sig := signature(p)
			if _, ok := m[sig]; !ok {
				m[sig] = p
			}
		}
		return m
	}

	accounts := make([]string, 0, len(allAccounts))
	for a := range allAccounts {
		accounts = append(accounts, a)
	}
	sort.Strings(accounts)

	for _, acct := range accounts {
		o := oldMap[acct]
		n := newMap[acct]
		om := patternMap(o)
		nm := patternMap(n)

		var added, removed []PatternRef
		for sig, p := range nm {
			if _, ok := om[sig]; !ok {
				added = append(added, toRef(p))
			}
		}
		for sig, p := range om {
			if _, ok := nm[sig]; !ok {
				removed = append(removed, toRef(p))
			}
		}
		sortRefs := func(rs []PatternRef) {
			sort.Slice(rs, func(i, j int) bool {
				if rs[i].Signature != rs[j].Signature {
					return rs[i].Signature < rs[j].Signature
				}
				return rs[i].Detail < rs[j].Detail
			})
		}
		sortRefs(added)
		sortRefs(removed)

		cd := AccountDiff{
			Account:         acct,
			OldScore:        o.Score,
			NewScore:        n.Score,
			OldLevel:        o.Level,
			NewLevel:        n.Level,
			LevelChanged:    o.Level != n.Level,
			AddedPatterns:   added,
			RemovedPatterns: removed,
		}
		if cd.Changed() {
			d.Changed = append(d.Changed, cd)
		} else {
			d.Unchanged = append(d.Unchanged, acct)
		}
	}
	return d
}
