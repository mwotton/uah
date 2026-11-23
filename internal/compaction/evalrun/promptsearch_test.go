package evalrun

import (
	"bytes"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/viktordanov/uah/internal/compaction"
)

func TestPromptSearchDoesNotSendOracle(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body struct {
			State     map[string]any                         `json:"state"`
			Questions map[string]compaction.DecisionQuestion `json:"questions"`
		}
		require.NoError(t, json.UnmarshalRead(r.Body, &body))
		state, _ := json.Marshal(body.State)
		require.NotContains(t, string(state), "want_pruned")
		require.NotContains(t, string(state), "validation-")
		answers := map[string]compaction.PruneAnswer{}
		for id, q := range body.Questions {
			require.Contains(t, q.Instructions, `"call_`)
			answers[id] = compaction.PruneAnswer{Type: "choice", Choice: "retain", Confidence: 1, Probabilities: map[string]float64{"prune": 0, "retain": 1}}
		}
		_ = json.MarshalWrite(w, map[string]any{"model": "elephant-1", "answers": answers})
	}))
	defer server.Close()
	model := compaction.Elephant{Settings: compaction.PruneSettings{Endpoint: server.URL, MaxContextTokens: 14000}}
	variants := []PruningPromptVariant{{Name: "test", PruneLabel: "prune", Instructions: "Judge %q", Criteria: map[string]string{"prune": "redundant", "retain": "needed"}}}
	var out bytes.Buffer
	require.NoError(t, PromptSearch(t.Context(), &out, model, variants, "validation", 1))
	require.Equal(t, 12, requests)
	require.Equal(t, 24, len(strings.Split(strings.TrimSpace(out.String()), "\n")))
	require.Contains(t, out.String(), `"want_pruned":true`)
}

func TestPruningSearchValidationBalance(t *testing.T) {
	cases := PruningSearchValidationCases()
	require.Len(t, cases, 12)
	positives := 0
	large := 0
	for _, c := range cases {
		if c.WantPruned {
			positives++
		}
		candidate, ok := searchCandidate(c.Case.Before, c.TargetID)
		require.True(t, ok)
		if len(compaction.ResultText(candidate.Result)) > 10000 {
			large++
		}
	}
	require.Equal(t, 6, positives)
	require.Equal(t, 2, large)
}

func TestPruningEvidenceTextKeepsContentsAndChronology(t *testing.T) {
	c := PruningSearchValidationCases()[0]
	candidate, ok := searchCandidate(c.Case.Before, c.TargetID)
	require.True(t, ok)
	text := PruningEvidenceText(candidate)
	require.Contains(t, text, compaction.ResultText(candidate.Result))
	require.Contains(t, text, "Implement a client using the current service schema.")
	require.Contains(t, text, `CALL id="later-0"`)
	require.Less(t, strings.Index(text, "TARGET RESULT"), strings.Index(text, "LATER HISTORY"))
	require.NotContains(t, text, "want_pruned")
}

func TestPruningFinalValidationOraclesHaveUniqueOriginalEvidence(t *testing.T) {
	cases := PruningSearchFinalCases()
	require.Len(t, cases, 12)
	positives := 0
	for _, c := range cases {
		if c.WantPruned {
			positives++
		}
		candidate, ok := searchCandidate(c.Case.Before, c.TargetID)
		require.True(t, ok)
		require.NotEmpty(t, candidate.Call.Arguments)
		if c.Case.Name == "final-long-original" {
			require.Contains(t, compaction.ResultText(candidate.Result), "capacity 2547")
			var later strings.Builder
			for _, item := range candidate.Later {
				writePruningItem(&later, item)
			}
			require.NotContains(t, later.String(), "capacity 2547")
			require.Contains(t, later.String(), "capacity 8191")
		}
	}
	require.Equal(t, 6, positives)
}
