package compaction

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/viktordanov/uah-core/harness/llm"
)

func pruneCall(id, name, args string) llm.Item {
	return llm.Item{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: id, Name: name, Arguments: args}}
}
func pruneResult(id, text string) llm.Item {
	return llm.Item{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: id, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: text}}}}
}
func pruneInput() []llm.Item {
	text := strings.Repeat("package demo; var value = 42\n", 100)
	return []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleSystem, Text: "system"}}, pruneCall("a", "Read", "file.go"), pruneResult("a", text), pruneCall("b", "Read", "file.go"), pruneResult("b", text)}
}
func pruneSettings() PruneSettings {
	return PruneSettings{Enabled: true, MinSavedTokens: 1, MinCalls: 1, MinSavingsRatio: .001}
}
func TestPrunerReplayRewindAndRawHistory(t *testing.T) {
	input := pruneInput()
	hash, _ := Hash(input)
	journal := filepath.Join(t.TempDir(), "pruning.jsonl")
	p := Pruner{Settings: pruneSettings(), Classifier: DuplicateClassifier{}, Journal: journal}
	out, pass := p.Rewrite(t.Context(), input)
	require.Empty(t, pass.Error)
	require.Less(t, EstimateTokens(out), EstimateTokens(input))
	require.Len(t, out, len(input))
	got, _ := Hash(input)
	require.Equal(t, hash, got)
	resumed := Pruner{Settings: pruneSettings(), Journal: journal}
	records, corrupt, readErr := readLines(journal, "pruning", func(v PrunePass) bool { return len(v.Decisions) > 0 })
	require.NoError(t, readErr)
	require.Zero(t, corrupt)
	require.NotEmpty(t, records)
	for _, rec := range records {
		for _, d := range rec.Decisions {
			require.True(t, validDecision(input, d), "%+v", d)
		}
	}
	replay, replayPass := resumed.Rewrite(t.Context(), input)
	require.Empty(t, replayPass.Error)
	require.Equal(t, out, replay)
	rewind, _ := resumed.Rewrite(t.Context(), input[:3])
	require.Equal(t, input[:3], rewind)
	branch := append([]llm.Item{}, input...)
	branch[4] = pruneResult("b", "different evidence")
	changed, _ := resumed.Rewrite(t.Context(), branch)
	require.Equal(t, branch, changed)
}
func TestPrunerSafety(t *testing.T) {
	for _, tc := range []struct{ name, text, tool string }{
		{"skill", strings.Repeat("payload ", 100), "SkillUse"},
		{"instructions", strings.Repeat("payload ", 100) + "You must keep the build offline", "Read"},
		{"negative", strings.Repeat("payload ", 100) + "no matches found", "Read"},
		{"unresolved", strings.Repeat("payload ", 100) + "FAIL TestValue", "Bash"},
		{"active", strings.Repeat("payload ", 100) + "Process running with session id 23", "Bash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := pruneInput()
			input[1] = pruneCall("a", tc.tool, "x")
			input[2] = pruneResult("a", tc.text)
			require.Empty(t, PruneCandidates(input, 12000))
		})
	}
	input := pruneInput()
	require.Empty(t, PruneCandidates(input[:3], 12000), "unprocessed result")
	parallel := []llm.Item{input[0], input[1], input[3], input[2], input[4]}
	require.Empty(t, PruneCandidates(parallel, 12000), "parallel sibling is not a witness")
	incomplete := input[:4]
	require.Empty(t, PruneCandidates(incomplete, 12000))
	require.Empty(t, PruneCandidates(input, 100), "no partial evidence")
}

type brokenClassifier struct{}

func (brokenClassifier) Classify(context.Context, []PruneCandidate) (map[string]PruneAnswer, error) {
	return nil, errors.New("service down")
}
func TestPrunerGatesAndFallback(t *testing.T) {
	for _, mode := range []string{"disabled", "shadow", "budget", "cadence", "ratio", "service", "disk"} {
		t.Run(mode, func(t *testing.T) {
			s := pruneSettings()
			p := Pruner{Settings: s, Classifier: DuplicateClassifier{}}
			switch mode {
			case "disabled":
				p.Settings.Enabled = false
			case "shadow":
				p.Settings.Shadow = true
			case "budget":
				p.Settings.MinSavedTokens = 100000
			case "cadence":
				p.Settings.MinCalls = 10
			case "ratio":
				p.Settings.MinSavingsRatio = 100
			case "service":
				p.Classifier = brokenClassifier{}
			case "disk":
				p.Journal = filepath.Join(t.TempDir(), "missing", "journal")
			}
			input := pruneInput()
			out, _ := p.Rewrite(t.Context(), input)
			require.Equal(t, input, out)
		})
	}
}

type resolvedClassifier struct{}

func (resolvedClassifier) Classify(_ context.Context, cs []PruneCandidate) (map[string]PruneAnswer, error) {
	out := map[string]PruneAnswer{}
	for _, c := range cs {
		out[c.ID] = PruneAnswer{Type: "choice", Choice: "resolved", Confidence: 1, Probabilities: map[string]float64{"resolved": 1}}
	}
	return out, nil
}
func TestPrunerResolvedFailureKeepsLesson(t *testing.T) {
	input := pruneInput()
	input[1] = pruneCall("a", "Bash", `{"command":"go test ./..."}`)
	input[2] = pruneResult("a", "FAIL TestValue: want 2 got 1\n"+strings.Repeat("trace data\n", 300)+"\nExit code: 1")
	input[3] = pruneCall("b", "Bash", `{"command":"go test ./..."}`)
	input[4] = pruneResult("b", "ok example/demo")
	p := Pruner{Settings: pruneSettings(), Classifier: resolvedClassifier{}}
	out, pass := p.Rewrite(t.Context(), input)
	require.Empty(t, pass.Error)
	require.Less(t, EstimateTokens(out), EstimateTokens(input))
	text := ResultText(out[2].Data.(llm.ToolResult))
	require.Contains(t, text, "Lesson from original failure")
	require.Contains(t, text, "FAIL TestValue: want 2 got 1")
	// A successful unrelated command never proves this test was fixed.
	input[3] = pruneCall("b", "Bash", `{"command":"go test ./unrelated"}`)
	require.Empty(t, PruneCandidates(input, 12000))
}
func TestPrunerBatchedCommitUsesCommitEvidence(t *testing.T) {
	input := pruneInput()
	journal := filepath.Join(t.TempDir(), "prune.jsonl")
	settings := pruneSettings()
	settings.MinCalls = 4
	p := Pruner{Settings: settings, Classifier: DuplicateClassifier{}, Journal: journal}
	out, _ := p.Rewrite(t.Context(), input)
	require.Equal(t, input, out)
	extended := append(append([]llm.Item{}, input...), pruneCall("c", "Read", "other.go"), pruneResult("c", "other"), pruneCall("d", "Read", "last.go"), pruneResult("d", "last"))
	out, _ = p.Rewrite(t.Context(), extended)
	require.Less(t, EstimateTokens(out), EstimateTokens(extended))
	resumed := Pruner{Settings: settings, Journal: journal}
	earlier, _ := resumed.Rewrite(t.Context(), input)
	require.Equal(t, input, earlier, "a later batch commit cannot affect an earlier request")
	replay, _ := resumed.Rewrite(t.Context(), extended)
	require.Equal(t, out, replay)
}

type uncertainClassifier struct{ probability bool }

func (u uncertainClassifier) Classify(_ context.Context, cs []PruneCandidate) (map[string]PruneAnswer, error) {
	a := PruneAnswer{Type: "choice", Choice: "superseded", Confidence: .97, Probabilities: map[string]float64{"superseded": 1}}
	if u.probability {
		a.Confidence = 1
		a.Probabilities["superseded"] = .97
	}
	out := map[string]PruneAnswer{}
	for _, c := range cs {
		out[c.ID] = a
	}
	return out, nil
}
func TestPrunerRequiresConfidenceAndProbability(t *testing.T) {
	for _, prob := range []bool{false, true} {
		p := Pruner{Settings: pruneSettings(), Classifier: uncertainClassifier{probability: prob}}
		input := pruneInput()
		out, _ := p.Rewrite(t.Context(), input)
		require.Equal(t, input, out)
	}
}
