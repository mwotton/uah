package compaction

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/viktordanov/uah-core/harness/llm"
)

// PruneSettings enables experimental request-only semantic elision. Zero is off.
// A table is replaced as a whole by config layering.
type PruneSettings struct {
	DecisionSchema      string  `toml:"decision_schema"`
	ReuseRequests       int     `toml:"reuse_requests"`
	EstimatedPrefillTPS float64 `toml:"estimated_prefill_tps"`
	Enabled             bool    `toml:"enabled"`
	Shadow              bool    `toml:"shadow"`
	Endpoint            string  `toml:"endpoint"`
	Model               string  `toml:"model"`
	APIKeyEnv           string  `toml:"api_key_env"`
	TimeoutMS           int     `toml:"timeout_ms"`
	Confidence          float64 `toml:"confidence"`
	MaxContextTokens    int     `toml:"max_context_tokens"`
	MinSavedTokens      int64   `toml:"min_saved_tokens"`
	MinCalls            int     `toml:"min_calls"`
	MinSavingsRatio     float64 `toml:"min_savings_ratio"`
}

func (s PruneSettings) Defaults() PruneSettings {
	if s.DecisionSchema == "" {
		s.DecisionSchema = "lifetime-v1"
	}
	if s.ReuseRequests == 0 {
		s.ReuseRequests = 4
	}
	if s.EstimatedPrefillTPS == 0 {
		s.EstimatedPrefillTPS = 1000
	}
	if s.Model == "" {
		s.Model = "elephant-1"
	}
	if s.TimeoutMS == 0 {
		s.TimeoutMS = 1500
	}
	if s.Confidence == 0 {
		s.Confidence = .98
	}
	if s.MaxContextTokens == 0 {
		s.MaxContextTokens = 12000
	}
	if s.MinSavedTokens == 0 {
		s.MinSavedTokens = 512
	}
	if s.MinCalls == 0 {
		s.MinCalls = 4
	}
	if s.MinSavingsRatio == 0 {
		s.MinSavingsRatio = .25
	}
	return s
}

func (s PruneSettings) Validate() error {
	s = s.Defaults()
	if (s.DecisionSchema != "lifetime-v1" && s.DecisionSchema != "binary-v1" && s.DecisionSchema != "necessity-v1" && s.DecisionSchema != "necessity-guarded-v1") || s.ReuseRequests < 1 || s.EstimatedPrefillTPS <= 0 || s.TimeoutMS < 1 || s.Confidence < .5 || s.Confidence > 1 || s.MaxContextTokens < 128 || s.MaxContextTokens > 14000 || s.MinSavedTokens < 1 || s.MinCalls < 1 || s.MinSavingsRatio < 0 || s.MinSavingsRatio > 1 {
		return fmt.Errorf("invalid semantic_pruning thresholds")
	}
	if s.Enabled {
		_, err := localEndpoint(s.Endpoint)
		return err
	}
	return nil
}

type PruneCandidate struct {
	Instructions []llm.Item     `json:"instructions"`
	ID           string         `json:"id"`
	Call         llm.ToolCall   `json:"call"`
	Result       llm.ToolResult `json:"result"`
	Later        []llm.Item     `json:"later"`
	Index        int            `json:"-"`
}

type PruneAnswer struct {
	GuardNeeded       *float64           `json:"guard_needed,omitempty"`
	GuardChoice       string             `json:"guard_choice,omitempty"`
	Vetoed            bool               `json:"vetoed,omitempty"`
	SourceChoice      string             `json:"source_choice,omitempty"`
	DerivedConfidence bool               `json:"derived_confidence,omitempty"`
	Noul              *float64           `json:"noul,omitempty"`
	Model             string             `json:"model,omitempty"`
	Type              string             `json:"type"`
	Choice            string             `json:"choice"`
	Probabilities     map[string]float64 `json:"probabilities"`
	Confidence        float64            `json:"confidence"`
}

type PruneClassifier interface {
	Classify(context.Context, []PruneCandidate) (map[string]PruneAnswer, error)
}

// PruneDecision is evidence-bound. A rewind before EvidenceCount, or any
// changed evidence, invalidates it. Call IDs alone are insufficient.
type PruneDecision struct {
	DecisionSchema string      `json:"decision_schema,omitempty"`
	ID             string      `json:"id"`
	Answer         PruneAnswer `json:"answer"`
	EvidenceCount  int         `json:"evidence_count"`
	EvidenceHash   string      `json:"evidence_hash"`
	Applied        bool        `json:"applied"`
}

type PrunePass struct {
	Classifications int             `json:"classifications"`
	Failures        int             `json:"failures"`
	Decisions       []PruneDecision `json:"decisions"`
	LatencyMS       float64         `json:"latency_ms"`
	Saved, Suffix   int64
	Error           string `json:"error,omitempty"`
}

// Pruner owns no transcript data. Its journal is a sibling of the compaction
// log, using the same crash-tolerant, fsynced record writer.
type Pruner struct {
	Settings   PruneSettings
	Classifier PruneClassifier
	Journal    string
	decisions  []PruneDecision
	loaded     bool
	lastCalls  int
}

var pruningExit = regexp.MustCompile(`(?m)^(?:Exit code: |Process exited with code )(-?\d+)\s*$`)

func protected(c llm.ToolCall, r llm.ToolResult) bool {
	name := strings.ToLower(c.Name)
	switch name {
	case "bash", "functions.bash", "read", "readfile", "read_file", "grep", "glob":
	default:
		return true // skills, operations, arbitrary tools: never candidates
	}
	text := strings.ToLower(c.Arguments + "\n" + ResultText(r))
	for _, marker := range []string{"agents.md", "claude.md", "skill.md", "skilluse", "instruction", "should ", "constraint", "decision", "policy", "rules", "system prompt", "developer message", "ignore previous", "must ", "do not ", "never ", "not found", "no matches", "0 matches", "running", "process running", "session id", "timed out", "background", "process group", "operation", "shell id", "listening on", "ensure ", "always ", "keep ", "required", "please ", "you are", "<system", "<developer"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	if strings.TrimSpace(ResultText(r)) == "" {
		return true
	}
	for _, o := range r.Output {
		if o.Kind != llm.ToolResultText {
			return true
		}
	}
	// Native Bash omits the exit line on success; failures carry Exit code.

	return false
}

// PruneCandidates requires a later completed call issued AFTER this result.
// Parallel siblings issued before it never establish model consumption.
// Keep complete later evidence; do not truncate failures out of the classifier's
// view. Oversized histories conservatively produce no candidate.
func PruneCandidates(input []llm.Item, maxTokens int) []PruneCandidate {
	calls := map[string]llm.ToolCall{}
	positions := map[string]int{}
	results := map[string]int{}
	for i, item := range input {
		if c, ok := item.Data.(llm.ToolCall); ok {
			calls[c.CallID] = c
			positions[c.CallID] = i
		}
		if r, ok := item.Data.(llm.ToolResult); ok {
			results[r.CallID] = i
		}
	}
	var out []PruneCandidate
	for i, item := range input {
		r, ok := item.Data.(llm.ToolResult)
		if !ok {
			continue
		}
		c, known := calls[r.CallID]
		if !known || protected(c, r) || EstimateTokens([]llm.Item{item}) < 100 {
			continue
		}
		witnessed := false
		for id, pos := range positions {
			if pos > i && results[id] > pos {
				witnessed = true
			}
		}
		if !witnessed {
			continue
		}
		if failedPruneResult(r) {
			resolved := false
			for id, later := range calls {
				if positions[id] > i && later.Name == c.Name && later.Arguments == c.Arguments && results[id] > positions[id] {
					result := input[results[id]].Data.(llm.ToolResult)
					exits := pruningExit.FindAllStringSubmatch(ResultText(result), -1)
					nativeSuccess := len(exits) == 0 && strings.EqualFold(later.Name, "Bash") && !protected(later, result)
					if (nativeSuccess || len(exits) > 0 && exits[len(exits)-1][1] == "0") && !failedPruneResult(result) {
						resolved = true
					}
				}
			}
			if !resolved {
				continue
			}
		}

		// Include intervening user constraints as well as later outcomes.
		var instructions []llm.Item
		for _, prior := range input[:positions[r.CallID]] {
			if msg, ok := prior.Data.(llm.Message); ok && msg.Role != llm.RoleAssistant {
				instructions = append(instructions, prior)
			}
		}
		evidence := append(slices.Clone(instructions), input[positions[r.CallID]:]...)
		if EstimateTokens(evidence) > int64(maxTokens) {
			continue
		}
		out = append(out, PruneCandidate{Instructions: instructions, ID: r.CallID, Call: c, Result: r, Later: slices.Clone(input[i+1:]), Index: i})
	}
	return out
}

func validDecision(input []llm.Item, d PruneDecision) bool {
	if d.EvidenceCount > len(input)-1 || d.EvidenceCount < 1 {
		return false
	}
	h, err := Hash(input[1 : 1+d.EvidenceCount])
	return err == nil && h == d.EvidenceHash
}

func (p *Pruner) Rewrite(ctx context.Context, input []llm.Item) ([]llm.Item, PrunePass) {
	s := p.Settings.Defaults()
	var pass PrunePass
	if !s.Enabled || len(input) < 2 {
		return input, pass
	}
	if !p.loaded {
		if p.Journal != "" {
			records, corrupt, err := readLines(p.Journal, "pruning", func(v PrunePass) bool { return len(v.Decisions) > 0 })
			if err != nil || corrupt > 0 {
				pass.Error = "unreadable pruning journal"
				return input, pass
			}
			for _, rec := range records {
				p.decisions = append(p.decisions, rec.Decisions...)
			}
		}
		p.loaded = true
	}
	var applied []string
	known := map[string]bool{}
	var pending []PruneDecision
	for _, d := range p.decisions {
		if !validDecision(input, d) {
			continue
		}
		if d.EvidenceCount == len(input)-1 {
			known[d.ID] = true
		}
		if d.Applied {
			applied = append(applied, d.ID)
			callsAtPass := 0
			for _, item := range input[1 : 1+d.EvidenceCount] {
				if _, ok := item.Data.(llm.ToolCall); ok {
					callsAtPass++
				}
			}
			p.lastCalls = max(p.lastCalls, callsAtPass)
			known[d.ID] = true
		} else if pruneChoice(d.Answer.Choice) {
			pending = append(pending, d)
			known[d.ID] = true
		}
	}
	view := PruneElide(input, applied)
	candidates := PruneCandidates(view, s.MaxContextTokens)
	var fresh []PruneCandidate
	for _, c := range candidates {
		if !known[c.ID] {
			fresh = append(fresh, c)
		}
	}
	// Bound each request. The service has a 16,384-token aggregate budget;
	// one complete evidence window per request avoids duplicated batch state.
	for i, c := range fresh {
		if i >= 4 {
			break
		}
		if p.Classifier == nil {
			break
		}
		pass.Classifications++
		started := time.Now()
		answers, err := p.Classifier.Classify(ctx, []PruneCandidate{c})
		pass.LatencyMS += float64(time.Since(started).Microseconds()) / 1000
		if err != nil {
			pass.Failures++
			pass.Error = err.Error()
			break
		}
		a, ok := answers[c.ID]
		if !ok {
			pass.Error = "missing classification"
			continue
		}
		h, err := Hash(input[1:])
		if err != nil {
			continue
		}
		d := PruneDecision{DecisionSchema: s.DecisionSchema, ID: c.ID, Answer: a, EvidenceCount: len(input) - 1, EvidenceHash: h}
		pass.Decisions = append(pass.Decisions, d)
		if pruneChoice(a.Choice) && a.Confidence >= s.Confidence && a.Probabilities[a.Choice] >= s.Confidence {
			pending = append(pending, d)
		}
	}
	var ids []string
	earliest := len(view)
	for _, d := range pending {
		if slices.Contains(applied, d.ID) {
			continue
		}
		if d.Answer.Confidence < s.Confidence || d.Answer.Probabilities[d.Answer.Choice] < s.Confidence {
			continue
		}
		for i, item := range view {
			if r, ok := item.Data.(llm.ToolResult); ok && r.CallID == d.ID {
				ids = append(ids, d.ID)
				earliest = min(earliest, i)
			}
		}
	}
	proposed := PruneElide(view, ids)
	pass.Saved = EstimateTokens(view) - EstimateTokens(proposed)
	pass.Suffix = EstimateTokens(proposed[earliest:])
	calls := 0
	for _, item := range input {
		if _, ok := item.Data.(llm.ToolCall); ok {
			calls++
		}
	}
	if pass.Saved >= s.MinSavedTokens && calls-p.lastCalls >= s.MinCalls && float64(pass.Saved) >= float64(pass.Suffix)*s.MinSavingsRatio && float64(pass.Saved)*float64(s.ReuseRequests) >= float64(pass.Suffix)+pass.LatencyMS*s.EstimatedPrefillTPS/1000 && !s.Shadow {
		for _, d := range pending {
			if slices.Contains(ids, d.ID) {
				d.Applied = true
				d.EvidenceCount = len(input) - 1
				d.EvidenceHash, _ = Hash(input[1:])
				pass.Decisions = append(pass.Decisions, d)
			}
		}
		view = proposed
	}
	if len(pass.Decisions) > 0 {
		// Persist before publishing a modified request. Failed writes retain originals.
		if p.Journal != "" {
			if err := appendLine(p.Journal, "pruning", pass); err != nil {
				pass.Error = err.Error()
				return PruneElide(input, applied), pass
			}
		}
		p.decisions = append(p.decisions, pass.Decisions...)
	}
	if len(ids) > 0 && !s.Shadow && len(view) > 0 && EstimateTokens(view) < EstimateTokens(PruneElide(input, applied)) {
		p.lastCalls = calls
	}
	if s.Shadow {
		return input, pass
	}
	return view, pass
}

// DuplicateClassifier is the deterministic control: identical later call and
// result, with a model-consumption witness already required by eligibility.
type DuplicateClassifier struct{}

func (DuplicateClassifier) Classify(_ context.Context, cs []PruneCandidate) (map[string]PruneAnswer, error) {
	out := map[string]PruneAnswer{}
	for _, c := range cs {
		choice := "retain"
		same := map[string]bool{}
		for _, item := range c.Later {
			if call, ok := item.Data.(llm.ToolCall); ok && call.Name == c.Call.Name && call.Arguments == c.Call.Arguments {
				same[call.CallID] = true
			}
			if r, ok := item.Data.(llm.ToolResult); ok && same[r.CallID] && ResultText(r) == ResultText(c.Result) {
				choice = "superseded"
			}
		}
		out[c.ID] = PruneAnswer{Type: "choice", Choice: choice, Confidence: 1, Probabilities: map[string]float64{choice: 1}}
	}
	return out, nil
}

// A resolved failure keeps a bounded verbatim lesson, never generated prose.
func failedPruneResult(r llm.ToolResult) bool {
	text := strings.ToLower(ResultText(r))
	exits := pruningExit.FindAllStringSubmatch(ResultText(r), -1)
	return (len(exits) > 0 && exits[len(exits)-1][1] != "0") || strings.Contains(text, "fail") || strings.Contains(text, "error")
}
func PruneElide(input []llm.Item, ids []string) []llm.Item {
	out := Elide(input, ids)
	selected := set(ids)
	for i, item := range input {
		r, ok := item.Data.(llm.ToolResult)
		if !ok || !selected[r.CallID] || !failedPruneResult(r) {
			continue
		}
		text := ResultText(r)
		if len(text) > 1024 {
			text = clip(text, 512) + "\n[excerpt gap]\n" + clipStart(text, 512)
		}
		stub := out[i].Data.(llm.ToolResult)
		stub.Output = append(slices.Clone(stub.Output), llm.ToolResultOutput{Kind: llm.ToolResultText, Value: "Lesson from original failure (verbatim excerpt):\n" + text})
		out[i].Data = stub
	}
	return out
}

func pruneChoice(choice string) bool {
	return choice == "prune" || choice == "superseded" || choice == "resolved"
}
