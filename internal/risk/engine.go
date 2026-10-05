package risk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"

	"github.com/highcumontoa/onchain-risk-engine-go/internal/graph"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/ledger"
)

// AccountRisk 是单个账户的风险结论，含可核对依据。
type AccountRisk struct {
	Account  string
	Score    int
	Level    ScoreLevel
	Patterns []graph.Pattern
}

// Report 是一次分析的完整、确定性结论。
// 不包含任何时间戳/随机字段：同一规则版本 + 同一输入 => 逐字节一致。
type Report struct {
	RuleVersion RuleVersion
	InputHash   string
	Accounts    []AccountRisk
}

// Engine 是并发安全的风险分析引擎：规则可更新，结论可回放。
type Engine struct {
	mu      sync.RWMutex
	current RuleSet
	history []RuleSet // 曾生效的规则版本，按生效顺序保存
	byVer   map[int]RuleSet
}

// NewEngine 使用给定规则集创建引擎，该规则集成为首个历史版本。
func NewEngine(rules RuleSet) *Engine {
	rules = normalize(rules)
	return &Engine{
		current: rules,
		history: []RuleSet{rules},
		byVer:   map[int]RuleSet{rules.Version.Version: rules},
	}
}

// normalize 填充缺省值并复制权重 map，保证 RuleSet 自包含、不可变。
func normalize(r RuleSet) RuleSet {
	if r.FanInSources <= 0 {
		r.FanInSources = 3
	}
	if r.FanOutTargets <= 0 {
		r.FanOutTargets = 3
	}
	if r.MaxCycleDepth <= 0 {
		r.MaxCycleDepth = 32
	}
	w := make(map[graph.PatternKind]int, len(r.Weights))
	for k, v := range r.Weights {
		w[k] = v
	}
	if len(w) == 0 {
		for k, v := range defaultWeights() {
			w[k] = v
		}
	}
	r.Weights = w
	return r
}

// inputHash 对规范化输入做稳定哈希，作为报告可核对的输入指纹。
func inputHash(txs []ledger.Tx) string {
	h := sha256.New()
	for _, t := range txs {
		fmt.Fprintf(h, "%s|%d|%s|%s|%d\n", t.ID, t.Timestamp, t.From, t.To, t.Amount)
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:8]) // 64 bit 指纹，日志友好
}

// analyze 是纯函数式分析：给定规则与交易列表，输出确定性报告。
func analyze(rules RuleSet, txs []ledger.Tx) Report {
	edges := make([]graph.Edge, len(txs))
	for i, t := range txs {
		edges[i] = graph.Edge{TxID: t.ID, From: t.From, To: t.To, Amount: t.Amount, Time: t.Timestamp}
	}
	g := graph.Build(edges)

	// 按模式类别收集，保证遍历顺序固定。
	all := []graph.Pattern{}
	all = append(all, g.DetectFanIn(rules.FanInSources)...)
	all = append(all, g.DetectFanOut(rules.FanOutTargets)...)
	all = append(all, g.DetectCycles(rules.MaxCycleDepth)...)
	all = append(all, g.DetectHotspots(rules.HotspotAmount)...)

	byAccount := map[string][]graph.Pattern{}
	accountSet := map[string]struct{}{}
	for _, p := range all {
		byAccount[p.Account] = append(byAccount[p.Account], p)
		accountSet[p.Account] = struct{}{}
	}

	accounts := make([]string, 0, len(accountSet))
	for a := range accountSet {
		accounts = append(accounts, a)
	}
	sort.Strings(accounts)

	rep := Report{RuleVersion: rules.Version, InputHash: inputHash(txs)}
	kindOrder := []graph.PatternKind{
		graph.PatternFanIn, graph.PatternFanOut,
		graph.PatternCycle, graph.PatternHotspot,
	}
	for _, acct := range accounts {
		ps := byAccount[acct]
		// 模式按 (类别, 依据字符串) 排序，确保依据列表顺序确定。
		sort.SliceStable(ps, func(i, j int) bool {
			ri, rj := rankOf(kindOrder, ps[i].Kind), rankOf(kindOrder, ps[j].Kind)
			if ri != rj {
				return ri < rj
			}
			return ps[i].Detail < ps[j].Detail
		})
		score := 0
		for _, p := range ps {
			score += rules.Weights[p.Kind]
		}
		if score > 100 {
			score = 100
		}
		rep.Accounts = append(rep.Accounts, AccountRisk{
			Account:  acct,
			Score:    score,
			Level:    LevelForScore(score),
			Patterns: ps,
		})
	}
	return rep
}

func rankOf(order []graph.PatternKind, k graph.PatternKind) int {
	for i, x := range order {
		if x == k {
			return i
		}
	}
	return len(order)
}

// AnalyzeTxs 用给定规则集对一批规范化交易做纯函数式分析。
// 这是全量与增量共用的唯一分析入口：增量视图只要保证其边集与“当前全量
// 已接受交易”相同，调用本函数得到的报告就与全量重算逐字节一致。
func AnalyzeTxs(rules RuleSet, txs []ledger.Tx) Report {
	return analyze(normalize(rules), txs)
}

// Analyze 对账本快照做确定性分析；并发调用结论必须一致。
func (e *Engine) Analyze(l *ledger.Ledger) Report {
	e.mu.RLock()
	rules := e.current
	e.mu.RUnlock()
	return analyze(rules, l.Txs())
}

// AnalyzeGraph 对图做分析（供模拟场景使用）；输入指纹基于图的边。
func (e *Engine) AnalyzeGraph(g *graph.Graph) Report {
	e.mu.RLock()
	rules := e.current
	e.mu.RUnlock()

	txs := make([]ledger.Tx, 0, len(g.Edges()))
	for _, ed := range g.Edges() {
		txs = append(txs, ledger.Tx{
			ID: ed.TxID, Timestamp: ed.Time, From: ed.From, To: ed.To, Amount: ed.Amount,
		})
	}
	rep := analyze(rules, txs)
	return rep
}

// CurrentRules 返回当前生效规则集快照。
func (e *Engine) CurrentRules() RuleSet {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.current
}

// UpdateRules 原子切换规则集，返回生效后的版本。
// 新版本自动登记到历史，供 Replay 追溯；同版本号重复提交不会重复登记。
func (e *Engine) UpdateRules(next RuleSet) RuleVersion {
	next = normalize(next)
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.byVer[next.Version.Version]; !ok {
		e.history = append(e.history, next)
		e.byVer[next.Version.Version] = next
	}
	e.current = next
	return next.Version
}

// Replay 使用指定的历史规则集对同一份输入复现结论，不改变当前生效规则。
// 若该版本未登记，仍按传入规则计算（可复现未来/假设规则），但不写入历史。
func (e *Engine) Replay(rules RuleSet, l *ledger.Ledger) Report {
	return analyze(normalize(rules), l.Txs())
}

// History 返回曾经生效过的规则版本（按登记顺序）。
func (e *Engine) History() []RuleSet {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]RuleSet, len(e.history))
	copy(out, e.history)
	return out
}
