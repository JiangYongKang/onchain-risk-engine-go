package rules

import (
	"fmt"
	"sync"

	"github.com/highcumontoa/onchain-risk-engine-go/graph"
	"github.com/highcumontoa/onchain-risk-engine-go/ledger"
)

// Conclusion 是一次带规则版本标记的历史结论，保证可追溯。
type Conclusion struct {
	RuleVersion int
	Report      graph.Report
}

// Replayer 维护规则版本历史，并支持对同一输入回放旧/新规则。
// 历史结论只追加不覆盖，旧结论始终可追溯。
type Replayer struct {
	mu      sync.RWMutex
	rules   []RuleSet    // 按版本递增
	history []Conclusion // 全部历史结论，追加不覆盖
}

// NewReplayer 以初始规则集创建回放器。
func NewReplayer(initial RuleSet) *Replayer {
	return &Replayer{rules: []RuleSet{initial}}
}

// UpdateRules 追加新版本规则（版本号必须严格递增）。
func (r *Replayer) UpdateRules(rs RuleSet) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	last := r.rules[len(r.rules)-1]
	if rs.Version <= last.Version {
		return fmt.Errorf("rule version must increase: got %d, current %d", rs.Version, last.Version)
	}
	r.rules = append(r.rules, rs)
	return nil
}

// current 返回当前规则集。
func (r *Replayer) current() RuleSet {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.rules[len(r.rules)-1]
}

// Run 用当前规则分析输入并记录结论。
func (r *Replayer) Run(tx []ledger.Transfer) Conclusion {
	rs := r.current()
	report := NewEngine(rs).Analyze(tx)
	c := Conclusion{RuleVersion: rs.Version, Report: report}
	r.mu.Lock()
	r.history = append(r.history, c)
	r.mu.Unlock()
	return c
}

// Replay 用指定历史版本规则重新分析同一输入，结论同样入历史。
// 对同一 (version, input) 的回放结果可复现。
func (r *Replayer) Replay(version int, tx []ledger.Transfer) (Conclusion, error) {
	r.mu.RLock()
	var rs RuleSet
	found := false
	for _, v := range r.rules {
		if v.Version == version {
			rs = v
			found = true
			break
		}
	}
	r.mu.RUnlock()
	if !found {
		return Conclusion{}, fmt.Errorf("unknown rule version %d", version)
	}
	report := NewEngine(rs).Analyze(tx)
	c := Conclusion{RuleVersion: rs.Version, Report: report}
	r.mu.Lock()
	r.history = append(r.history, c)
	r.mu.Unlock()
	return c, nil
}

// History 返回全部历史结论（含回放），按产生顺序。
func (r *Replayer) History() []Conclusion {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Conclusion, len(r.history))
	copy(out, r.history)
	return out
}
