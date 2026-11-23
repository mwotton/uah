package evalrun

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/compaction/eval"
)

// PruningStrategy uses precisely the production eligibility and rewrite policy.
// Each recorded cut is independent, like the existing elision strategies.
func PruningStrategy(name string, settings compaction.PruneSettings, classifier compaction.PruneClassifier) Strategy {
	return Strategy{Name: name, Apply: func(ctx context.Context, c eval.Case, _ *Summaries) ([]llm.Item, int64, error) {
		p := compaction.Pruner{Settings: settings, Classifier: classifier}
		out, pass := p.Rewrite(ctx, c.Before)
		if pass.Error != "" {
			return out, 0, fmt.Errorf("pruning classification failed: %s", pass.Error)
		}
		return out, 0, nil
	}}
}

type PruningCase struct {
	TargetID      string
	RequiredCalls []string
	Case          eval.Case
	// WantPruned is a manually authored outcome oracle. It is not sent to
	// Elephant. Resolved historical failures are intentionally discardable.
	WantPruned bool
}

// PruningCases cover information lifetime independently of model prompts.
// Smallest cases exercise gates; the real recorded fixture remains available.
func PruningCases() []PruningCase {
	msg := func(role llm.Role, text string) llm.Item {
		return llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: role, Text: text}}
	}
	call := func(id, name, args string) llm.Item {
		return llm.Item{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: id, Name: name, Arguments: args}}
	}
	result := func(id, text string) llm.Item {
		return llm.Item{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: id, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: text}}}}
	}
	large := strings.Repeat("row: alpha beta gamma delta\n", 110)
	cases := []struct {
		name, tool, args, old, newArgs, newText string
		want                                    bool
	}{
		{"corrected-command", "Bash", "cat missing.go", "Exit code: 1\ncat: missing.go not found\n" + large, "cat main.go", "Exit code: 0\n" + large, true},
		{"fixed-test", "Bash", "go test ./...", "Exit code: 1\nFAIL TestValue: want 2 got 1\n" + large, "go test ./...", "Exit code: 0\nPASS TestValue; corrected value to 2", true},
		{"unresolved-test", "Bash", "go test ./...", "Exit code: 1\nFAIL TestValue: want 2 got 1\n" + large, "go test ./unrelated", "Exit code: 0\nPASS unrelated test", false},
		{"duplicate-read", "Read", "source.go", large, "source.go", large, true},
		{"superseded-file", "Read", "source.go", large, "source.go", "version 2, replaced alpha with omega\n" + large, true},
		{"overlapping-search", "Grep", "alpha source.go", large, "alpha .", large + "extra.go: alpha", true},
		{"negative-evidence", "Grep", "security bypass", "no matches found\n" + large, "source.go", large, false},
		{"obsolete-large-success", "Bash", "git status", "Exit code: 0\n" + large, "git status", "Exit code: 0\nworking tree clean", true},
		{"instructions", "Read", "guide.txt", "You must keep networking disabled.\n" + large, "guide.txt", large, false},
		{"skill", "SkillUse", "workflow", large, "workflow", large, false},
	}
	var out []PruningCase
	for _, tc := range cases {
		items := []llm.Item{msg(llm.RoleSystem, "You are a coding agent."), msg(llm.RoleUser, "Fix value.go and verify TestValue; keep security constraints."), call("old", tc.tool, tc.args), result("old", tc.old), call("new", tc.tool, tc.newArgs), result("new", tc.newText), msg(llm.RoleAssistant, "I have processed the results.")}
		if tc.name == "fixed-test" {
			prefix := append([]llm.Item{}, items[:4]...)
			prefix = append(prefix, call("patch", "apply_patch", "*** Update File: value.go\n-var value = 1\n+var value = 2"), result("patch", "Success. Updated value.go"))
			items = append(prefix, items[4:]...)
		}
		items = append(items, call("check1", "Read", "value.go"), result("check1", "value = 2"), call("check2", "Read", "README.md"), result("check2", "done"))
		out = append(out, PruningCase{Case: eval.Case{Name: tc.name, Before: items, Window: 16384}, WantPruned: tc.want})
	}
	// Neither a sibling's completion nor an unfinished operation proves that a
	// model read the old output.
	duplicate := out[3].Case.Before
	parallel := append([]llm.Item{}, duplicate[:2]...)
	parallel = append(parallel, duplicate[2], duplicate[4], duplicate[3], duplicate[5])
	out = append(out, PruningCase{Case: eval.Case{Name: "parallel", Before: parallel, Window: 16384}})
	out = append(out, PruningCase{Case: eval.Case{Name: "incomplete", Before: duplicate[:5], Window: 16384}})
	return out
}

// PruningHeldOutCases were authored after freezing the initial prompt and
// thresholds. They test retention rather than rewarding removal of old data.
func PruningHeldOutCases() []PruningCase {
	base := PruningCases()
	comparison := base[4]
	comparison.Case.Name = "heldout-comparison"
	comparison.WantPruned = false
	comparison.Case.Before = append([]llm.Item{}, comparison.Case.Before...)
	comparison.Case.Before[1] = llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "Compare source.go before and after the edit. Include the original values and explain each change."}}
	flaky := base[1]
	flaky.Case.Name = "heldout-flaky-test"
	flaky.WantPruned = false
	flaky.Case.Before = append([]llm.Item{}, flaky.Case.Before...)
	flaky.Case.Before = append(flaky.Case.Before[:4:4], flaky.Case.Before[6:]...)
	flaky.Case.Before[1] = llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "Investigate an intermittent TestValue failure. A passing rerun alone does not establish a fix. Find the root cause."}}
	disjoint := base[5]
	disjoint.Case.Name = "heldout-disjoint-search"
	disjoint.WantPruned = false
	disjoint.Case.Before = append([]llm.Item{}, disjoint.Case.Before...)
	disjoint.Case.Before[4] = llm.Item{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "new", Name: "Grep", Arguments: "beta docs/"}}
	disjoint.Case.Before[5] = llm.Item{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: "new", Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: strings.Repeat("docs/help.md: beta\n", 110)}}}}
	disjoint.Case.Before[1] = llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "Investigate both alpha source references and beta documentation references. Report all matching paths."}}
	return []PruningCase{comparison, flaky, disjoint}
}

// PruningExperiment extends the existing measurement functions with latency,
// failures and a separate outcome oracle. This times offline rewrites, not Qwen.
// Five rotating repetitions reduce ordering effects; no live prefill is implied.
func PruningExperiment(ctx context.Context, w io.Writer, settings compaction.PruneSettings, remote compaction.PruneClassifier, heldout bool, caseSets ...string) error {
	settings.Enabled = true
	variants := []struct {
		name       string
		classifier compaction.PruneClassifier
	}{
		{"none", nil}, {"uah automatic", nil}, {"heuristic", compaction.DuplicateClassifier{}}, {"elephant", compaction.Elephant{Settings: settings}},
	}
	if remote != nil {
		variants[3].classifier = remote
	}
	fmt.Fprintln(w, "Offline synthetic rewrites; token estimates use UAH's estimator. No Qwen calls. Five rotating repetitions.")
	fmt.Fprintln(w, "case,strategy,repeat,before_tokens,after_tokens,uncached_suffix_tokens,logical_input_tokens,rewrite_ms,classifier_ms,errors,pruned,want_pruned,classifications,failed_requests,old_choice,old_confidence,refetch,refetch_total,classifier_input_tokens,classifier_cost_usd,old_probability,required_outputs_lost")
	cases := PruningCases()
	if heldout {
		cases = PruningHeldOutCases()
	}
	if len(caseSets) > 0 && caseSets[0] != "" && caseSets[0] != "synthetic" {
		if caseSets[0] != "final-validation" {
			return fmt.Errorf("unknown pruning experiment set %q", caseSets[0])
		}
		cases = PruningSearchFinalCases()
		// Two completed terminal checks bring the cut to the default four-call
		// batching cadence without introducing replacement facts for the task.
		for i := range cases {
			input := append([]llm.Item{}, cases[i].Case.Before...)
			for n, cmd := range []string{"pwd", "git status --porcelain"} {
				id := fmt.Sprintf("terminal-check-%d", n)
				input = append(input, llm.Item{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: id, Name: "Bash", Arguments: fmt.Sprintf(`{"command":%q}`, cmd)}}, llm.Item{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: id, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "/work/project; investigation continues"}}}})
			}
			cases[i].Case.Before = input
		}
	}
	for rep := 0; rep < 5; rep++ {
		for _, tc := range cases {
			for j := range variants {
				v := variants[(j+rep)%len(variants)]
				after := tc.Case.Before
				var pass compaction.PrunePass
				var usage compaction.DecisionUsage
				if model, ok := v.classifier.(compaction.Elephant); ok {
					model.Observe = func(u compaction.DecisionUsage) { usage.InputTokens += u.InputTokens; usage.CostUSD += u.CostUSD }
					v.classifier = model
				}
				start := time.Now()
				callInput := int64(0)
				if v.name == "uah automatic" && compaction.EstimateTokens(tc.Case.Before) >= compaction.AutoLimit(tc.Case.Window, compaction.DefaultAutoPercent) {
					var err error
					after, callInput, err = automatic(compaction.DefaultElision, compaction.DefaultKeepCalls)(ctx, tc.Case, &Summaries{})
					if err != nil {
						return err
					}
				} else if v.classifier != nil {
					p := compaction.Pruner{Settings: settings, Classifier: v.classifier}
					after, pass = p.Rewrite(ctx, tc.Case.Before)
				}
				elapsed := float64(time.Since(start).Microseconds()) / 1000
				r := eval.Measure(tc.Case, after)
				errors := 0
				if pass.Error != "" {
					errors = 1
				}
				targetID := tc.TargetID
				if targetID == "" {
					targetID = "old"
				}
				pruned := false
				for _, item := range after {
					if result, ok := item.Data.(llm.ToolResult); ok && result.CallID == targetID {
						for _, original := range tc.Case.Before {
							if old, ok := original.Data.(llm.ToolResult); ok && old.CallID == targetID {
								pruned = compaction.ResultText(result) != compaction.ResultText(old)
							}
						}
					}
				}
				choice := "unclassified"
				confidence := 0.0
				probability := 0.0
				for _, d := range pass.Decisions {
					if d.ID == targetID {
						choice = d.Answer.Choice
						confidence = d.Answer.Confidence
						probability = d.Answer.Probabilities[d.Answer.Choice]
					}
				}
				requiredLost := 0
				for _, id := range tc.RequiredCalls {
					original, remaining := "", ""
					for _, item := range tc.Case.Before {
						if r, ok := item.Data.(llm.ToolResult); ok && r.CallID == id {
							original = compaction.ResultText(r)
						}
					}
					for _, item := range after {
						if r, ok := item.Data.(llm.ToolResult); ok && r.CallID == id {
							remaining = compaction.ResultText(r)
						}
					}
					if original != remaining {
						requiredLost++
					}
				}
				fmt.Fprintf(w, "%s,%s,%d,%d,%d,%d,%d,%.3f,%.3f,%d,%t,%t,%d,%d,%s,%.6f,%d,%d,%d,%.9f,%.6f,%d\n", tc.Case.Name, v.name, rep, r.Before, r.After, r.CacheMiss, r.After+callInput, elapsed, pass.LatencyMS, errors, pruned, tc.WantPruned, pass.Classifications, pass.Failures, choice, confidence, r.Refetch.Found, r.Refetch.Total, usage.InputTokens, usage.CostUSD, probability, requiredLost)
			}
		}
	}
	return nil
}
