package evalrun

import (
	_ "embed"
	"encoding/json/v2"
	"fmt"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah/internal/compaction/eval"
)

//go:embed testdata/pruning-search-final.json
var finalPruningScenarios []byte

// PruningSearchFinalCases was frozen before the last mutation round. Expected
// answers stay outside inference. First validation is now development feedback.
func PruningSearchFinalCases() []PruningCase {
	var specs []struct {
		Name, Task, Tool, Args, Original string
		Later                            []struct{ Tool, Args, Result string }
		WantPruned                       bool `json:"want_pruned"`
	}
	// The fixture uses lowercase keys; use explicit case-insensitive matching only
	// for this hand-authored fixture, never for external decision responses.
	if err := json.Unmarshal(finalPruningScenarios, &specs, json.MatchCaseInsensitiveNames(true)); err != nil {
		panic(err)
	}
	msg := func(role llm.Role, text string) llm.Item {
		return llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: role, Text: text}}
	}
	call := func(id, name, args string) llm.Item {
		return llm.Item{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: id, Name: name, Arguments: args}}
	}
	result := func(id, text string) llm.Item {
		return llm.Item{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: id, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: text}}}}
	}
	var out []PruningCase
	for i, s := range specs {
		target := fmt.Sprintf("call_%016x", i+1921)
		input := []llm.Item{msg(llm.RoleSystem, "You are a coding assistant."), msg(llm.RoleUser, s.Task), call(target, s.Tool, s.Args), result(target, s.Original)}
		for n, step := range s.Later {
			id := fmt.Sprintf("follow_%d", n)
			input = append(input, call(id, step.Tool, step.Args), result(id, step.Result))
		}
		required := []string{fmt.Sprintf("follow_%d", len(s.Later)-1)}
		if !s.WantPruned {
			required = append(required, target)
		}
		out = append(out, PruningCase{RequiredCalls: required, TargetID: target, Case: eval.Case{Name: s.Name, Before: input, Window: 16384}, WantPruned: s.WantPruned})
	}
	return out
}
