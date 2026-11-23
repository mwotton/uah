package evalrun

import (
	"fmt"
	"strings"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah/internal/compaction/eval"
)

// PruningSearchValidationCases is frozen before prompt screening. It includes
// positive and negative cases, different names/data from development, and two
// larger evidence windows. Do not use validation feedback to select mutations.
func PruningSearchValidationCases() []PruningCase {
	type step struct{ tool, args, text string }
	type scenario struct {
		name, task, tool, args, old string
		later                       []step
		want                        bool
	}
	config := `{"port":7319,"max_attempts":7,"queue_limit":63,"idle_seconds":29,"retry_seconds":11,"backend":"cedar"}`
	config = strings.Repeat(config+"\n", 8)
	source := strings.Repeat("const shard = 17; const region = 'cedar';\n", 30)
	cases := []scenario{
		{"validation-schema-copy", "Implement a client using the current service schema.", "Read", "schema.json", config, []step{{"Read", "schema.json", config}}, true},
		{"validation-search-superset", "List every reference to enqueue in the repository.", "Grep", "enqueue src/", "src/a.go: enqueue(task)\n" + source, []step{{"Grep", "enqueue .", "src/a.go: enqueue(task)\n" + source + "test/b.go: enqueue(sample)"}}, true},
		{"validation-current-source", "Finish updating the parser. Only the final implementation is needed; no migration comparison.", "Read", "parse.go", source, []step{{"apply_patch", "parse.go", "Changed shard from 17 to 23."}, {"Read", "parse.go", strings.ReplaceAll(source, "17", "23")}}, true},
		{"validation-final-diff", "Prepare the final patch description from the current diff.", "Bash", "git diff", source, []step{{"apply_patch", "parse.go", "Applied final edit."}, {"Bash", "git diff", source + "+const mode = 'queue';\n"}}, true},
		{"validation-fixed-lint", "Fix the reported lint issue and verify the same lint target.", "Bash", "golangci-lint run", "lint.go:9: unused variable scratch\n" + source + "\nExit code: 1", []step{{"apply_patch", "lint.go", "Removed unused variable scratch."}, {"Bash", "golangci-lint run", "Exit code: 0\nNo diagnostics."}}, true},
		{"validation-truncated-reread", "Find the backend and queue limit from the configuration file.", "Read", "settings.json", config, []step{{"Read", "settings.json", "{\"port\":7319}"}}, false},
		{"validation-independent-evidence", "Find references in both server and client packages.", "Grep", "enqueue server/", "server/dispatch.go: enqueue(job)\n" + source, []step{{"Grep", "enqueue client/", "client/send.go: enqueue(request)\n" + source}}, false},
		{"validation-regression-comparison", "Explain which original parser behavior changed and why.", "Read", "parse.go", "func decode(token string) bool { return len(token) > 3 && token[0] == 'C' }\n" + source, []step{{"Bash", "python rewrite_parser.py --mode exact", "Rewrote parse.go."}, {"Read", "parse.go", "func decode(token string) bool { return token == 'CEDAR' }\n" + source}}, false},
		{"validation-intermittent", "Find the root cause of nondeterministic allocation-test results.", "Bash", "go test ./allocator", "allocation_test.go:91: expected 7 got 9\n" + source + "\nExit code: 1", []step{{"Bash", "go test ./allocator", "ok allocator\nExit code: 0"}}, false},
		{"validation-absent-symbol", "Determine whether a parser named CedarDecoder exists and where BirchDecoder is defined.", "Grep", "CedarDecoder .", "No matches for CedarDecoder in the repository.\n" + source, []step{{"Grep", "BirchDecoder .", "decoder/birch.go: type BirchDecoder struct {}"}}, false},
	}
	var long strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&long, "record %03d: component cedar uses value %d for region birch and version %d.\n", i, i*13+7, i%17)
	}
	cases = append(cases,
		scenario{"validation-long-copy", "Read the catalog and extract the current component values.", "Read", "catalog.txt", long.String(), []step{{"Read", "catalog.txt", long.String()}}, true},
		scenario{"validation-long-missing-fact", "Report the original value of component 147 as well as its new value.", "Read", "catalog.txt", long.String(), []step{{"Bash", "python rewrite_catalog.py --record 147 --value 2049", "Updated catalog record 147."}, {"Read", "catalog.txt", strings.ReplaceAll(long.String(), "value 1918", "value 2049")}}, false},
	)
	message := func(role llm.Role, text string) llm.Item {
		return llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: role, Text: text}}
	}
	call := func(id, name, args string) llm.Item {
		return llm.Item{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: id, Name: name, Arguments: args}}
	}
	result := func(id, text string) llm.Item {
		return llm.Item{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: id, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: text}}}}
	}
	var out []PruningCase
	for index, s := range cases {
		targetID := fmt.Sprintf("call_%016x", index+701)
		input := []llm.Item{message(llm.RoleSystem, "You are a coding assistant."), message(llm.RoleUser, s.task), call(targetID, s.tool, s.args), result(targetID, s.old)}
		for i, step := range s.later {
			id := fmt.Sprintf("later-%d", i)
			input = append(input, call(id, step.tool, step.args), result(id, step.text))
		}
		out = append(out, PruningCase{TargetID: targetID, Case: eval.Case{Name: s.name, Before: input, Window: 16384}, WantPruned: s.want})
	}
	return out
}
