package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"
	"github.com/viktordanov/uah-core/harness/llm"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/compaction/eval"
	"github.com/viktordanov/uah/internal/compaction/evalrun"
	"github.com/viktordanov/uah/internal/home"
)

// compactionCommand is the hidden `uah compaction eval`: the compaction
// strategies compared on recorded sessions, as tables of numbers.
func compactionCommand() *cli.Command {
	return &cli.Command{
		Name:   "compaction",
		Usage:  "evaluate compaction on recorded sessions",
		Hidden: true,
		Commands: []*cli.Command{{
			Name:      "eval",
			Usage:     "compare the compaction strategies on recorded sessions",
			ArgsUsage: "[session file or directory]",
			Description: "Cuts each session (default: every session in uah's home) at its recorded compactions and at the\n" +
				"requests that first reached 50k, 100k, 150k, and 200k input tokens, captures the request the\n" +
				"embedded engine builds there, and applies each strategy. Summaries come from the sessions' own\n" +
				"compaction logs, the cache, or a stub; --summary-model writes the missing ones with a real model and\n" +
				"caches them. Prints tables of numbers only, never session content.",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "pruning-search-state", Value: "native", Usage: "prompt-search evidence presentation: native or text"},
				&cli.StringFlag{Name: "pruning-prompt-dir", Usage: "run a prompt search using variants.json in this directory"},
				&cli.StringFlag{Name: "pruning-search-set", Value: "development", Usage: "prompt-search cases: development, augmented, validation, or final-validation"},
				&cli.IntFlag{Name: "pruning-search-repeats", Value: 1, Usage: "prompt-search repetitions (1 to 10)"},
				&cli.Float64Flag{Name: "pruning-confidence", Value: .98, Usage: "experimental confidence and chosen-probability threshold"},
				&cli.StringFlag{Name: "pruning-schema", Value: "lifetime-v1", Usage: "decision schema: lifetime-v1, binary-v1, necessity-v1, or necessity-guarded-v1"},
				&cli.BoolFlag{Name: "pruning-heldout", Usage: "use held-out retention cases with --pruning-experiment"},
				&cli.StringFlag{Name: "openrouter-decision-model", Usage: "explicitly evaluate a remote decision model (e.g. baseten/elephant-1); requires OPENROUTER_API_KEY"},
				&cli.StringFlag{Name: "pruning-experiment-set", Usage: "policy experiment cases: synthetic or final-validation"},
				&cli.BoolFlag{Name: "pruning-experiment", Usage: "run five offline synthetic pruning trials (CSV)"},
				&cli.StringFlag{Name: "elephant-endpoint", Usage: "local System One URL; enables Elephant comparison"},
				&cli.StringFlag{Name: "elephant-key-env", Usage: "environment variable holding the local service token"},
				&cli.StringFlag{Name: "cache", Usage: "the summary cache directory (default: <uah home>/cache/compaction-eval)"},
				&cli.Int64Flag{Name: "window", Usage: "the context window in tokens", Value: 272_000},
				&cli.StringFlag{Name: "summary-model", Usage: "write missing summaries with this model (real model calls)"},
				&cli.StringFlag{Name: "summary-provider", Usage: "the provider for --summary-model", Value: "openai-codex"},
				&cli.StringFlag{Name: "summary-effort", Usage: "the effort for --summary-model", Value: "medium"},
				&cli.StringFlag{Name: "prompt-file", Usage: "a summary prompt to evaluate instead of Codex's"},
				&cli.StringSliceFlag{Name: "strategy", Usage: "evaluate only this strategy (repeatable)"},
				&cli.Int64Flag{Name: "max-case-tokens", Usage: "skip cases whose request is larger (bounds --summary-model's cost)"},
			},
			OnUsageError: onUsageError,
			Action:       compactionEvalAction,
		}},
	}
}

func compactionEvalAction(ctx context.Context, cmd *cli.Command) error {
	prune := compaction.PruneSettings{DecisionSchema: cmd.String("pruning-schema"), Confidence: cmd.Float64("pruning-confidence"), Enabled: true, Endpoint: cmd.String("elephant-endpoint"), APIKeyEnv: cmd.String("elephant-key-env")}
	validation := prune
	validation.Enabled = false
	if err := validation.Validate(); err != nil {
		return cli.Exit(err.Error(), exitUsage)
	}
	var remote compaction.PruneClassifier
	if model := cmd.String("openrouter-decision-model"); model != "" {
		classifier := compaction.OpenRouterPruneClassifier(model, "OPENROUTER_API_KEY")
		classifier.Settings.DecisionSchema = prune.DecisionSchema
		classifier.Settings.Confidence = prune.Confidence
		remote = classifier
	}
	if dir := cmd.String("pruning-prompt-dir"); dir != "" {
		classifier, ok := remote.(compaction.Elephant)
		if !ok {
			return cli.Exit("prompt search requires --openrouter-decision-model", exitUsage)
		}
		classifier.Settings.MaxContextTokens = 14000
		variants, err := evalrun.LoadPruningPrompts(dir)
		if err != nil {
			return err
		}
		return evalrun.PromptSearch(ctx, os.Stdout, classifier, variants, cmd.String("pruning-search-set"), cmd.Int("pruning-search-repeats"), cmd.String("pruning-search-state"))
	}
	if cmd.Bool("pruning-experiment") {
		if prune.Endpoint == "" {
			prune.Endpoint = "http://127.0.0.1:4000/v1/systemone"
		}
		return evalrun.PruningExperiment(ctx, os.Stdout, prune, remote, cmd.Bool("pruning-heldout"), cmd.String("pruning-experiment-set"))
	}
	target := cmd.Args().First()
	if target == "" {
		target = filepath.Join(home.Dir(), "sessions")
	}
	cache := cmd.String("cache")
	if cache == "" {
		cache = filepath.Join(home.Dir(), "cache", "compaction-eval")
	}
	sums := &evalrun.Summaries{Dir: cache}
	if f := cmd.String("prompt-file"); f != "" {
		data, err := os.ReadFile(f)
		if err != nil {
			return cli.Exit(fmt.Sprintf("failed to read the prompt: %v", err), exitUsage)
		}
		sums.Prompt = strings.TrimSpace(string(data))
	}
	if model := cmd.String("summary-model"); model != "" {
		live, closeLive, err := evalrun.Live(cmd.String("summary-provider"), model, llm.ReasoningEffort(cmd.String("summary-effort")), sums.Prompt, os.Getenv)
		if err != nil {
			return err
		}
		defer func() { _ = closeLive() }()
		sums.Live = live
	}
	strategies := evalrun.Strategies()
	strategies = append(strategies, evalrun.PruningStrategy("eager heuristic", prune, compaction.DuplicateClassifier{}))
	if remote != nil {
		strategies = append(strategies, evalrun.PruningStrategy("eager elephant (OpenRouter)", prune, remote))
	}
	if prune.Endpoint != "" {
		strategies = append(strategies, evalrun.PruningStrategy("eager elephant", prune, compaction.Elephant{Settings: prune}))
	}
	rep, err := evalrun.Run(ctx, target, evalrun.Options{Summaries: sums, Strategies: evalrun.Only(strategies, cmd.StringSlice("strategy")), Window: cmd.Int64("window"), Progress: os.Stderr, MaxCaseTokens: cmd.Int64("max-case-tokens")})
	if err != nil {
		return err
	}
	w := os.Stdout
	fmt.Fprintf(w, "sessions: %d, cases: %d (skipped %d), recorded compactions reproduced: %d of %d\n",
		rep.Sessions, rep.Cases, rep.Skipped, rep.Reproduced, rep.Recorded)
	fmt.Fprintf(w, "summaries: %d recorded, %d cached, %d from the model, %d stubs\n\n",
		rep.Sources[evalrun.SourceRecorded], rep.Sources[evalrun.SourceCache], rep.Sources[evalrun.SourceModel], rep.Sources[evalrun.SourceStub])
	eval.Write(w, rep.Rows)

	return nil
}
