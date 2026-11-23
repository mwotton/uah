package embedded_test

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/testing/fakellm"
)

func TestEmbedded_SemanticPruning(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Questions map[string]any `json:"questions"`
		}
		if err := json.UnmarshalRead(r.Body, &req); err != nil {
			w.WriteHeader(400)
			return
		}
		answers := map[string]compaction.PruneAnswer{}
		for id := range req.Questions {
			answers[id] = compaction.PruneAnswer{Type: "choice", Choice: "superseded", Confidence: 1, Probabilities: map[string]float64{"active": 0, "superseded": 1, "resolved": 0, "retain": 0}}
		}
		_ = json.MarshalWrite(w, map[string]any{"model": "elephant-1", "answers": answers})
	}))
	defer service.Close()
	big := "head -c 8000 /dev/zero | tr '\\0' x"
	e := newEnv(t, fakellm.Reply{Commands: []string{big}}, fakellm.Reply{Commands: []string{big}}, fakellm.Reply{Text: "done"}, fakellm.Reply{Text: "again"}, fakellm.Reply{Text: "resumed"})
	eng := e.withCompaction(compaction.Settings{Semantic: compaction.PruneSettings{Enabled: true, Endpoint: service.URL, MaxContextTokens: 14000, MinSavedTokens: 1, MinCalls: 1, MinSavingsRatio: .001}}, 0)
	s, ev := e.open(t, eng, "")
	ask(t, s, ev, "first")
	reqs := e.llm.Requests()
	require.Len(t, reqs, 3)
	require.NotContains(t, reqs[1].ToolOutputs[0], "uah elided", "Qwen must first receive the original")
	if !strings.HasPrefix(reqs[2].ToolOutputs[0], "[uah elided") {
		text := reqs[2].ToolOutputs[0]
		t.Logf("output bytes=%d head=%q tail=%q", len(text), text[:min(150, len(text))], text[max(0, len(text)-150):])
	}
	require.True(t, strings.HasPrefix(reqs[2].ToolOutputs[0], "[uah elided"))
	require.NotContains(t, reqs[2].ToolOutputs[1], "uah elided", "newest result is protected")
	ask(t, s, ev, "second")
	id := s.ID()
	require.NoError(t, s.Close())
	resumed, events := e.open(t, eng, id)
	ask(t, resumed, events, "third")
	reqs = e.llm.Requests()
	require.Equal(t, reqs[2].ToolOutputs, reqs[4].ToolOutputs)
}

func TestEmbedded_SemanticPruningServiceFailure(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer service.Close()
	big := "head -c 8000 /dev/zero | tr '\\0' x"
	e := newEnv(t, fakellm.Reply{Commands: []string{big}}, fakellm.Reply{Commands: []string{big}}, fakellm.Reply{Text: "done"})
	eng := e.withCompaction(compaction.Settings{Semantic: compaction.PruneSettings{Enabled: true, Endpoint: service.URL, MaxContextTokens: 14000, MinCalls: 1, MinSavedTokens: 1, MinSavingsRatio: .001}}, 0)
	s, ev := e.open(t, eng, "")
	ask(t, s, ev, "first")
	reqs := e.llm.Requests()
	require.Len(t, reqs, 3)
	require.Len(t, reqs[2].ToolOutputs, 2)
	for _, out := range reqs[2].ToolOutputs {
		require.NotContains(t, out, "uah elided")
	}
}
