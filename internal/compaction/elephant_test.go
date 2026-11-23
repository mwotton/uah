package compaction

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestElephantSystemOne(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/systemone", r.URL.Path)
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		var body struct {
			Model     string         `json:"model"`
			Questions map[string]any `json:"questions"`
		}
		require.NoError(t, json.UnmarshalRead(r.Body, &body))
		require.Equal(t, "elephant-1", body.Model)
		require.Len(t, body.Questions, 1)
		_, _ = w.Write([]byte(`{"model":"elephant-1","answers":{"a":{"type":"choice","choice":"superseded","confidence":0.99,"probabilities":{"active":0.001,"superseded":0.995,"resolved":0.001,"retain":0.003}}}}`))
	}))
	defer server.Close()
	t.Setenv("PRUNE_TEST_KEY", "test-key")
	e := Elephant{Settings: PruneSettings{Endpoint: server.URL + "/v1/systemone", APIKeyEnv: "PRUNE_TEST_KEY"}}
	a, err := e.Classify(t.Context(), PruneCandidates(pruneInput(), 12000))
	require.NoError(t, err)
	require.Equal(t, "superseded", a["a"].Choice)
}
func TestElephantFallback(t *testing.T) {
	for _, body := range []string{`{}`, `{"model":"other"}`, `{"model":"elephant-1","answers":{"a":{"type":"choice","choice":"superseded","confidence":1,"probabilities":{"superseded":1}}}}`, `garbage`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		_, err := (Elephant{Settings: PruneSettings{Endpoint: srv.URL}}).Classify(t.Context(), PruneCandidates(pruneInput(), 12000))
		require.Error(t, err)
		srv.Close()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(30 * time.Millisecond) }))
	defer srv.Close()
	_, err := (Elephant{Settings: PruneSettings{Endpoint: srv.URL, TimeoutMS: 1}}).Classify(t.Context(), PruneCandidates(pruneInput(), 12000))
	require.Error(t, err)
	for _, url := range []string{"https://remote.example/v1/systemone", "http://192.168.1.1/", "http://localhost/", "http://127.0.0.1@evil.example/"} {
		_, err := localEndpoint(url)
		require.Error(t, err)
	}
}

func TestElephantBinaryPromptAndResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Questions map[string]struct {
				Instructions string            `json:"instructions"`
				Criteria     map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		require.NoError(t, json.UnmarshalRead(r.Body, &body))
		require.Contains(t, body.Questions["a"].Instructions, `candidate id is "a"`)
		require.Contains(t, body.Questions["a"].Instructions, "passing rerun alone")
		require.Len(t, body.Questions["a"].Criteria, 2)
		_, _ = w.Write([]byte(`{"model":"elephant-1","answers":{"a":{"type":"choice","choice":"prune","confidence":1,"probabilities":{"prune":1,"retain":0}}}}`))
	}))
	defer srv.Close()
	e := Elephant{Settings: PruneSettings{Endpoint: srv.URL, DecisionSchema: "binary-v1"}}
	input := pruneInput()
	settings := pruneSettings()
	settings.DecisionSchema = "binary-v1"
	p := Pruner{Settings: settings, Classifier: e}
	out, pass := p.Rewrite(t.Context(), input)
	require.Empty(t, pass.Error)
	require.Less(t, EstimateTokens(out), EstimateTokens(input))
}

func TestElephantDecideBatchesAndValidatesPerQuestion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			State     map[string]string           `json:"state"`
			Questions map[string]DecisionQuestion `json:"questions"`
		}
		require.NoError(t, json.UnmarshalRead(r.Body, &body))
		require.Equal(t, "identical evidence", body.State["evidence"])
		require.Len(t, body.Questions, 2)
		_, _ = w.Write([]byte(`{"model":"elephant-1","answers":{"one":{"type":"choice","choice":"drop","confidence":1,"probabilities":{"drop":1,"keep":0}},"two":{"type":"choice","choice":"retain","confidence":1,"probabilities":{"prune":0,"retain":1}}}}`))
	}))
	defer srv.Close()
	model := Elephant{Settings: PruneSettings{Endpoint: srv.URL}}
	answers, err := model.Decide(t.Context(), map[string]string{"evidence": "identical evidence"}, map[string]DecisionQuestion{
		"one": {Type: "choice", Criteria: map[string]string{"drop": "redundant", "keep": "needed"}},
		"two": {Type: "choice", Criteria: map[string]string{"prune": "redundant", "retain": "needed"}},
	})
	require.NoError(t, err)
	require.Equal(t, "drop", answers["one"].Choice)
	require.Equal(t, "retain", answers["two"].Choice)
	_, err = model.Decide(t.Context(), nil, nil)
	require.Error(t, err)
}

func TestElephantNoulPreservesProbability(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"elephant-1","answers":{"needed":{"type":"noul","noul":0.04}}}`))
	}))
	defer srv.Close()
	model := Elephant{Settings: PruneSettings{Endpoint: srv.URL}}
	answers, err := model.Decide(t.Context(), "same contents read again", map[string]DecisionQuestion{"needed": {Type: "noul", Instructions: "Is the separate original still needed?"}})
	require.NoError(t, err)
	a := answers["needed"]
	require.Equal(t, "noul", a.Type)
	require.Equal(t, "false", a.Choice)
	require.InDelta(t, .96, a.Probabilities["false"], .00001)
	require.InDelta(t, .92, a.Confidence, .00001)
	require.NotNil(t, a.Noul)
	require.Equal(t, .04, *a.Noul)
}

func TestElephantRequestEncodingIsCanonical(t *testing.T) {
	var previous string
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw bytes.Buffer
		_, err := raw.ReadFrom(r.Body)
		require.NoError(t, err)
		if count > 0 {
			require.Equal(t, previous, raw.String())
		}
		previous = raw.String()
		count++
		_, _ = w.Write([]byte(`{"model":"elephant-1","answers":{"q":{"type":"choice","choice":"no","confidence":1,"probabilities":{"yes":0,"no":1}}}}`))
	}))
	defer srv.Close()
	model := Elephant{Settings: PruneSettings{Endpoint: srv.URL}}
	for i := 0; i < 5; i++ {
		_, err := model.Decide(t.Context(), map[string]string{"z": "last", "a": "first"}, map[string]DecisionQuestion{"q": {Type: "choice", Instructions: "needed?", Criteria: map[string]string{"yes": "needed", "no": "duplicate"}}})
		require.NoError(t, err)
	}
	require.Equal(t, 5, count)
}

func TestElephantNecessityNormalizesLosslessDecision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Questions map[string]DecisionQuestion `json:"questions"`
		}
		require.NoError(t, json.UnmarshalRead(r.Body, &body))
		require.Equal(t, "noul", body.Questions["a"].Type)
		require.Contains(t, body.Questions["a"].Instructions, "separate original body")
		require.Empty(t, body.Questions["a"].Criteria)
		_, _ = w.Write([]byte(`{"model":"elephant-1","answers":{"a":{"type":"noul","noul":0.06}}}`))
	}))
	defer srv.Close()
	model := Elephant{Settings: PruneSettings{Endpoint: srv.URL, DecisionSchema: "necessity-v1"}}
	answers, err := model.Classify(t.Context(), PruneCandidates(pruneInput(), 12000))
	require.NoError(t, err)
	a := answers["a"]
	require.Equal(t, "false", a.SourceChoice)
	require.Equal(t, "prune", a.Choice)
	require.True(t, a.DerivedConfidence)
	require.InDelta(t, .94, a.Probabilities["prune"], .00001)
	require.InDelta(t, .88, a.Confidence, .00001)
	settings := pruneSettings()
	settings.DecisionSchema = "necessity-v1"
	settings.Confidence = .6
	p := Pruner{Settings: settings, Classifier: model}
	input := pruneInput()
	out, pass := p.Rewrite(t.Context(), input)
	require.Empty(t, pass.Error)
	require.Less(t, EstimateTokens(out), EstimateTokens(input))
}

func TestElephantMissingFactVeto(t *testing.T) {
	for _, needed := range []float64{.2, .5, .8} {
		t.Run(fmt.Sprint(needed), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Questions map[string]DecisionQuestion `json:"questions"`
				}
				require.NoError(t, json.UnmarshalRead(r.Body, &body))
				require.Len(t, body.Questions, 2)
				answers := map[string]any{}
				for id, q := range body.Questions {
					p := .01
					if strings.Contains(q.Instructions, "at least one fact") {
						p = needed
					}
					answers[id] = map[string]any{"type": "noul", "noul": p}
				}
				_ = json.MarshalWrite(w, map[string]any{"model": "elephant-1", "answers": answers})
			}))
			defer srv.Close()
			classifier := Elephant{Settings: PruneSettings{Endpoint: srv.URL, DecisionSchema: "necessity-guarded-v1"}}
			answers, err := classifier.Classify(t.Context(), PruneCandidates(pruneInput(), 12000))
			require.NoError(t, err)
			a := answers["a"]
			require.NotNil(t, a.GuardNeeded)
			require.Equal(t, needed, *a.GuardNeeded)
			require.InDelta(t, .99, a.Probabilities["prune"], .00001)
			if needed >= .5 {
				require.Equal(t, "retain", a.Choice)
				require.True(t, a.Vetoed)
			} else {
				require.Equal(t, "prune", a.Choice)
				require.False(t, a.Vetoed)
			}
		})
	}
}
