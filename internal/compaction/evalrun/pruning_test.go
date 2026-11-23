package evalrun_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/compaction/evalrun"
)

func TestPruningExperimentUsesProductionPolicy(t *testing.T) {
	for _, heldout := range []bool{false, true} {
		var out bytes.Buffer
		require.NoError(t, evalrun.PruningExperiment(context.Background(), &out, compaction.PruneSettings{}, compaction.DuplicateClassifier{}, heldout))
		_, body, _ := strings.Cut(out.String(), "\n")
		rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
		require.NoError(t, err)
		cases := 12
		if heldout {
			cases = 3
		}
		require.Len(t, rows, 1+cases*4*5)
		pruned := 0
		for _, row := range rows[1:] {
			if row[1] == "heuristic" && row[10] == "true" {
				pruned++
				require.Equal(t, "duplicate-read", row[0])
			}
		}
		if heldout {
			require.Zero(t, pruned)
		} else {
			require.Equal(t, 5, pruned)
		}
	}
}

func TestPruningPolicyFinalSetKeepsRequiredResults(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, evalrun.PruningExperiment(t.Context(), &out, compaction.PruneSettings{}, compaction.DuplicateClassifier{}, false, "final-validation"))
	_, body, _ := strings.Cut(out.String(), "\n")
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	require.NoError(t, err)
	lostColumn := -1
	for i, name := range rows[0] {
		if name == "required_outputs_lost" {
			lostColumn = i
		}
	}
	require.NotEqual(t, -1, lostColumn)
	count := 0
	for _, row := range rows[1:] {
		require.Equal(t, "0", row[lostColumn], "case %s, strategy %s", row[0], row[1])
		if row[1] == "heuristic" && row[10] == "true" {
			count++
			require.Equal(t, "final-long-copy", row[0])
		}
	}
	require.Equal(t, 5, count)
}
