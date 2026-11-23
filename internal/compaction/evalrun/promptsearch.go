package evalrun

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah/internal/compaction"
)

// PruningPromptVariant is an offline mutation, not a production setting.
type PruningPromptVariant struct {
	QuestionType string            `json:"question_type,omitempty"`
	Name         string            `json:"name"`
	File         string            `json:"file"`
	Criteria     map[string]string `json:"criteria"`
	PruneLabel   string            `json:"prune_label"`
	Instructions string            `json:"-"`
}

func LoadPruningPrompts(dir string) ([]PruningPromptVariant, error) {
	data, err := os.ReadFile(filepath.Join(dir, "variants.json"))
	if err != nil {
		return nil, err
	}
	var variants []PruningPromptVariant
	if err = json.Unmarshal(data, &variants); err != nil {
		return nil, err
	}
	if len(variants) < 1 || len(variants) > 16 {
		return nil, fmt.Errorf("need 1 to 16 variants")
	}
	seen := map[string]bool{}
	for i := range variants {
		v := &variants[i]
		if v.QuestionType == "" {
			v.QuestionType = "choice"
		}
		validChoice := v.QuestionType == "choice" && len(v.Criteria) == 2 && v.Criteria[v.PruneLabel] != ""
		validNoul := v.QuestionType == "noul" && len(v.Criteria) == 0 && (v.PruneLabel == "true" || v.PruneLabel == "false")
		if v.Name == "" || seen[v.Name] || (!validChoice && !validNoul) {
			return nil, fmt.Errorf("invalid or duplicate variant %q", v.Name)
		}
		seen[v.Name] = true
		data, err = os.ReadFile(filepath.Join(dir, v.File))
		if err != nil {
			return nil, err
		}
		v.Instructions = string(data)
	}
	return variants, nil
}

// PromptSearch compares mutations in independent questions sharing identical
// evidence. Raw answers are recorded even when production guards would retain
// the interaction. Expected labels NEVER enter the inference state.
func PromptSearch(ctx context.Context, w io.Writer, model compaction.Elephant, variants []PruningPromptVariant, split string, repeats int, formats ...string) error {
	if repeats < 1 || repeats > 10 {
		return fmt.Errorf("repeats must be 1 to 10")
	}
	format := "native"
	if len(formats) > 0 {
		format = formats[0]
	}
	if format != "native" && format != "text" {
		return fmt.Errorf("unknown state format %q", format)
	}
	var cases []PruningCase
	switch split {
	case "development":
		cases = append(PruningCases(), PruningHeldOutCases()...)
	case "validation":
		cases = PruningSearchValidationCases()
	case "augmented":
		cases = append(append(PruningCases(), PruningHeldOutCases()...), PruningSearchValidationCases()...)
	case "final-validation":
		cases = PruningSearchFinalCases()
	default:
		return fmt.Errorf("unknown search split %q", split)
	}
	encode := func(v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(data))
		return err
	}
	for rep := 0; rep < repeats; rep++ {
		for _, tc := range cases {
			candidate, ok := searchCandidate(tc.Case.Before, tc.TargetID)
			if !ok {
				return fmt.Errorf("no target in %s", tc.Case.Name)
			}
			eligible := false
			for _, c := range compaction.PruneCandidates(tc.Case.Before, model.Settings.Defaults().MaxContextTokens) {
				if c.ID == candidate.ID {
					eligible = true
				}
			}
			questions := map[string]compaction.DecisionQuestion{}
			for i := range variants {
				v := variants[(i+rep)%len(variants)]
				prompt := strings.ReplaceAll(v.Instructions, "%q", strconv.Quote(candidate.ID))
				questionType := v.QuestionType
				if questionType == "" {
					questionType = "choice"
				}
				questions[v.Name] = compaction.DecisionQuestion{Type: questionType, Instructions: prompt, Criteria: v.Criteria}
			}
			var usage compaction.DecisionUsage
			model.Observe = func(u compaction.DecisionUsage) { usage = u }
			started := time.Now()
			var state any = map[string]any{"candidates": []compaction.PruneCandidate{candidate}}
			if format == "text" {
				state = PruningEvidenceText(candidate)
			}
			requestBytes, _ := json.Marshal(map[string]any{"model": model.Settings.Defaults().Model, "state": state, "questions": questions}, json.Deterministic(true))
			requestHash := fmt.Sprintf("%x", sha256.Sum256(requestBytes))
			answers, err := model.Decide(ctx, state, questions)
			elapsed := float64(time.Since(started).Microseconds()) / 1000
			failure := ""
			if err != nil {
				failure = err.Error()
			}

			if e := encode(map[string]any{"request_sha256": requestHash, "state_format": format, "kind": "request", "split": split, "case": tc.Case.Name, "repeat": rep, "variants": len(questions), "latency_ms": elapsed, "usage": usage, "error": failure}); e != nil {
				return e
			}
			for _, v := range variants {
				a, exists := answers[v.Name]
				p := a.Probabilities[v.PruneLabel]
				rawPrune := a.Choice == v.PruneLabel
				// The actual production confidence/probability thresholds stay fixed.
				approved := rawPrune && a.Confidence >= .98 && p >= .98
				threshold := model.Settings.Defaults().Confidence
				selectedApproved := rawPrune && a.Confidence >= threshold && p >= threshold
				if e := encode(map[string]any{"state_format": format, "kind": "answer", "split": split, "case": tc.Case.Name, "repeat": rep, "variant": v.Name, "want_pruned": tc.WantPruned, "eligible": eligible, "valid": exists && err == nil, "answer": a, "prune_probability": p, "raw_prune": rawPrune, "approved": approved, "selected_threshold": threshold, "selected_approved": selectedApproved, "error": failure}); e != nil {
					return e
				}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	return nil
}

func searchCandidate(input []llm.Item, targetIDs ...string) (compaction.PruneCandidate, bool) {
	targetID := "old"
	if len(targetIDs) > 0 && targetIDs[0] != "" {
		targetID = targetIDs[0]
	}
	var call llm.ToolCall
	var instructions []llm.Item
	for i, item := range input {
		if c, ok := item.Data.(llm.ToolCall); ok && c.CallID == targetID {
			call = c
		}
		if r, ok := item.Data.(llm.ToolResult); ok && r.CallID == targetID && call.CallID != "" {
			return compaction.PruneCandidate{ID: targetID, Call: call, Result: r, Instructions: instructions, Later: input[i+1:], Index: i}, true
		}
		if m, ok := item.Data.(llm.Message); ok && m.Role != llm.RoleAssistant {
			instructions = append(instructions, item)
		}
	}
	return compaction.PruneCandidate{}, false
}

// PruningEvidenceText changes presentation, not evidence or its chronology.
// It is an offline input-format mutation, and never contains oracle labels.
func PruningEvidenceText(c compaction.PruneCandidate) string {
	var b strings.Builder
	b.WriteString("EARLIER MESSAGES: requirements of the coding task\n")
	for _, item := range c.Instructions {
		writePruningItem(&b, item)
	}
	fmt.Fprintf(&b, "\nTARGET CALL id=%q tool=%q arguments=%s\nTARGET RESULT:\n%s\n", c.ID, c.Call.Name, c.Call.Arguments, compaction.ResultText(c.Result))
	b.WriteString("\nLATER HISTORY, RETAINED IN ORDER:\n")
	for _, item := range c.Later {
		writePruningItem(&b, item)
	}
	return b.String()
}
func writePruningItem(b *strings.Builder, item llm.Item) {
	switch d := item.Data.(type) {
	case llm.Message:
		fmt.Fprintf(b, "MESSAGE role=%s:\n%s\n", d.Role, d.Text)
	case llm.ToolCall:
		fmt.Fprintf(b, "CALL id=%q tool=%q arguments=%s\n", d.CallID, d.Name, d.Arguments)
	case llm.ToolResult:
		fmt.Fprintf(b, "RESULT id=%q:\n%s\n", d.CallID, compaction.ResultText(d))
	default:
		fmt.Fprintf(b, "ITEM type=%s data=%+v\n", item.Type, item.Data)
	}
}
