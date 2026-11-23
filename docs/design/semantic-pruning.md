# Experimental semantic pruning

Disabled by default. This prototype tests whether a decision model can improve
working context before a session reaches its normal compaction threshold.
See `reports/semantic-pruning/report.md` for measured results and limitations.

## Enable

After the local service has its intended 16,384-token input limit:

```toml
[semantic_pruning]
enabled = true
shadow = true
endpoint = "http://127.0.0.1:4000/v1/systemone"
model = "elephant-1"
api_key_env = "LITELLM_API_KEY"
timeout_ms = 1500
confidence = 0.98
decision_schema = "binary-v1" # explicit revised prompt; default is lifetime-v1
max_context_tokens = 12000
min_saved_tokens = 512
min_calls = 4
min_savings_ratio = 0.25
reuse_requests = 4
estimated_prefill_tps = 1000
```

Use `shadow = false` to permit rewrites. This is not recommended routinely based
on the current experiment. The rate is an uncalibrated **prefill** estimate;
Qwen's roughly 100 decode tokens/s does not supply this number. Use the complete
System One URL. Production accepts loopback HTTP IP addresses only; proxies and
redirects are disabled. Nothing loads weights or changes GPU assignments.

Configuration layers replace this entire table, rather than merging its fields.
Zero numeric values select defaults. Validation rejects invalid thresholds and
nonlocal production endpoints. A service still limited to 2k can reject larger
requests; the request will remain intact.

## Decision prompts

`lifetime-v1` retains the original four labels for baseline reproduction.
`binary-v1` asks only `prune` or `retain`, names the target call ID, describes the
exact stub, and requires concrete evidence that replacement loses no needed
facts or lessons. It explicitly addresses before/after comparisons, disjoint
searches, flaky tests and uncertainty. Its full versioned prompt is
`internal/compaction/prompts/prune_binary.md`. Select it with
`--pruning-schema binary-v1` in the evaluator or `decision_schema = "binary-v1"`
in configuration. Decisions persist their schema and actual response model.

Elephant's reported choice confidence is its peak probability rescaled above
uniform, not an empirically calibrated safety probability. At threshold 0.98,
the confidence gate implies peak probability at least 0.985 for four labels
and 0.99 for two labels. Both confidence and selected probability gates apply.
Raw probabilities, action errors and physical pruning should all be compared.

## Request path and persistence

The existing embedded compactor applies its normal summary/elision record first.
An additional pass then classifies completed observational outputs, under the
request context, before forwarding to the model. Existing compaction behavior
and its threshold remain unchanged when this feature is off. Only tool result
bodies are replaced; calls, result IDs, all messages, reasoning and configuration
updates remain in place.

A result requires a later completed call **issued after the result**, proving
that a subsequent model response was possible. An earlier-issued parallel
sibling is insufficient. This deliberately delays pruning more than merely
seeing an assistant message. Unknown tools, skills, images, recognized
instructions, negative evidence and operation markers are protected. Shell
successes normally have no exit line in the current renderer; failures do.
Failures require an identical later command with a successful outcome
before becoming eligible. Native Bash success is recognized by its lack of a
failure exit line; its tool renderer omits the success exit line. A different passing test cannot resolve a failing one.
The classifier still must approve removal, at the configured confidence and
selected probability thresholds. A resolved failure retains a bounded verbatim
head/tail excerpt, command and exit metadata rather than a generated summary.
Changed-command corrections and many malformed tool calls conservatively remain.

The classifier receives the complete candidate and subsequent history, plus
preceding system/user/developer messages. No evidence is silently truncated.
If this exceeds the input allowance, keep the result. This limits coverage in
long histories. Instructions can be stated in arbitrary language: keyword guards
are conservative defenses, **not a proof of semantic classification safety**.
A flaky test can pass without resolution; that distinction remains a classifier
judgment, tested separately by a held-out case.

The journal is `<session>.compaction.jsonl.pruning.jsonl`, next to the existing
compaction log. It reuses its fsynced JSONL writer. It contains answers,
probabilities, latency, evidence hashes and counts, and physical-application
records. It never overwrites the transcript or operation outputs. A commit is
persisted before any rewritten request is published. Failed writes keep the
previous view. Corrupt journals conservatively disable new application.

Each decision hashes its complete supporting history after the system item;
a physical commit also binds to the history at commit time. Rewinds, branches,
rerendered outputs and new summaries that change this evidence invalidate it.
A later batch commit cannot change an earlier cut. Resume reapplies valid
commits without reclassifying. The commit's tool-call count restores cadence.
The classifier uses an append-only evidence window; changing later instructions
can require requesting the original output from the authoritative transcript.
Saved decisions are stable rather than silently being rejudged on every resume.

Stubs are deterministic and preserve tool protocol relationships. Original
outputs remain recoverable from the session and operation records. The existing
stub tells the model it can fetch data again. There is no separate recovery tool.
Disabling the feature restores the unpruned request (subject to normal compaction).

## Cache policy

Classification runs eagerly, with at most four candidates per pass and one
complete evidence window per HTTP request. The adapter also accepts multiple
questions, but production avoids repeating windows in a batch that exceeds the
model budget. Errors stop classification for that pass and retain the original
new outputs. Existing applied decisions continue to hold.

Newly approved decisions accumulate. Physical rewriting requires all of:

* At least `min_saved_tokens` net estimated savings.
* `min_calls` calls since the last physical pass.
* Savings at least `min_savings_ratio` times the rewritten suffix.
* Savings times `reuse_requests` at least suffix tokens plus classification
  milliseconds converted to tokens at `estimated_prefill_tps`.

Between commits the old prefix is stable. These are estimated item-boundary
cache costs, not Strata cache telemetry. The first rewrite pays to rebuild the
suffix. Future savings depend on reuse. Classification is synchronous and can
hurt latency even when no physical rewrite occurs. No background classification,
special cache-free compaction window, or automatic Strata calibration is included.

## Reproduce

```sh
# Use a workspace cache if the host's cache symlink has an unavailable target.
export GOCACHE="$PWD/.cache/go-build"
go test -race ./internal/compaction/... ./internal/config
go test ./internal/engine/embedded ./internal/app ./cmd/uah \
  -run 'Prun|Elephant|Compaction|Rewind|Clear|TestReference'

# Existing recorded fixture and fact/re-fetch tables, including the heuristic.
go run ./cmd/uah compaction eval internal/compaction/evalrun/testdata/sessions

# Explicit remote experiment. Load your existing key into the environment first.
# The ordinary agent configuration never sends context to OpenRouter.
go run ./cmd/uah compaction eval --pruning-experiment \
  --openrouter-decision-model baseten/elephant-1 > synthetic.csv
go run ./cmd/uah compaction eval --pruning-experiment --pruning-heldout \
  --openrouter-decision-model baseten/elephant-1 > heldout.csv

go run ./cmd/uah compaction eval \
  --openrouter-decision-model baseten/elephant-1 \
  internal/compaction/evalrun/testdata/sessions

# Add --pruning-schema binary-v1 to either experiment command to compare
# the revised prompt on the same cases.

# Local equivalent (after service startup).
go run ./cmd/uah compaction eval --pruning-experiment \
  --elephant-endpoint http://127.0.0.1:4000/v1/systemone \
  --elephant-key-env LITELLM_API_KEY
```

Synthetic trials use five rotating repetitions, the same request/cut and 16k
budget for every strategy. The default compaction strategy only runs when due.
The original recorded-session evaluator intentionally evaluates compaction as if
the cut reached its threshold, as it did before this prototype. Summaries can be
recorded, cached, generated on explicit request, or stubs; source counts matter.

CSV includes estimated context and invalidated suffix tokens, logical primary
input (not actual prefill work), client latency, actual decision-service input
tokens/cost, failures, raw classifications, physical pruning, and an independent
expected-discard oracle. Protected cases with `unclassified` do not count as
classifier successes. Cases with resolved historical failures expect removal;
preserving every failure is not scored as perfect retention. Held-out cases
were written after freezing the original prompt and thresholds.

The original fact metric finds path names, sometimes just basenames, in the
remaining text; leaving tool arguments can produce apparent recall without the
actual facts. The re-fetch metric detects later calls naming dropped paths; it
cannot show that forgetting caused those calls. Synthetic cases have no future
coding trajectory, so their zero re-fetch counts establish nothing about agent
behavior. Completion, repeated failed approaches, real KV reuse, measured Strata
prefill and coding-task wall time require the local coding endpoint. They are
not measured by this offline runner.

## Manual prompt search

The follow-up experiment is in `reports/semantic-pruning/prompt-search/`.
`iterations.md` records hypotheses and rejects; manifests pair each immutable
prompt with its option descriptions and prune-label mapping. This supports
native choice and boolean (`noul`) questions, sharing identical evidence in
one request for inexpensive screening. Default requests use canonical JSON;
confirmation records a request SHA256 to verify repeat identity. Batched
screening is not a per-variant latency measurement.

The selected `necessity-v1` prompt is in
`internal/compaction/prompts/prune_necessity.md`. It asks whether the separate
original body is still needed. P(false) is normalized to P(prune); native
P(true), actual model ID and the derived confidence marker remain in the log.
It uses the same eligibility and physical rewrite gates as other schemas.

```sh
# OPENROUTER_API_KEY must already be available. These are explicit remote runs.
go run ./cmd/uah compaction eval \
  --pruning-prompt-dir reports/semantic-pruning/prompt-search/finalists \
  --pruning-search-repeats 5 \
  --openrouter-decision-model baseten/elephant-1 > development.jsonl

# The selected mutation and baseline on the independent frozen validation set.
go run ./cmd/uah compaction eval \
  --pruning-prompt-dir reports/semantic-pruning/prompt-search/validation-comparison \
  --pruning-search-set validation --pruning-search-repeats 5 \
  --pruning-confidence 0.6 \
  --openrouter-decision-model baseten/elephant-1 > validation.jsonl

# Run the selected prompt through the actual request-rewriting policy.
go run ./cmd/uah compaction eval --pruning-experiment \
  --pruning-schema necessity-v1 --pruning-confidence 0.6 \
  --openrouter-decision-model baseten/elephant-1 > policy.csv
```

Search output separates raw answers, production eligibility, default .98
approval, and approval at an explicitly selected threshold. Inference errors
are invalid rows, not retention successes. Labels never enter the request state.
`--pruning-search-state text` tests a plain-text rendering of identical evidence;
its results must be compared with controls under that same representation.

The original held-out examples are now development cases. Twelve new validation
cases include different evidence, opaque call IDs, and two larger input windows.
The prompt and experimental threshold were selected before querying validation.
Do not tune further using that validation set and continue calling it held out.
A small synthetic validation cannot establish safe retention on arbitrary coding
sessions; shadow mode remains appropriate even if these examples pass.

### Prompt-search outcome and the guard

The first selected prompt at confidence 0.6 failed independent validation: it
proposed dropping a separate search and an original value needed for comparison.
That cutoff is rejected. The final selection, locked before a second validation,
is `necessity-guarded-v1`: a necessity question plus a missing-fact veto. Their
full prompts are `prompts/prune_necessity.md` and `prompts/prune_missing_fact.md`
under `internal/compaction/`. They share one request and each return P(true).

The primary question asks whether the separate original remains necessary;
P(false) is its prune probability. The second asks whether even one needed fact
is missing from later evidence. Its P(true) >= 0.5 forces retain. This conjunction
is a decision rule, not a calibrated joint posterior. The journal records both
probabilities and whether the guard vetoed the primary result. No text is generated.

On twelve new cases, the locked rule approved three of six disposable outputs
and retained all six required outputs over five identical repeats. Those are six
independent retention cases, not thirty independent observations. It is still a
small synthetic result. First validation is now development data; second
validation remains untouched after selection.

Decision snapshots and physical rewrites must be distinguished: adding later
calls can move model probabilities, and savings/cadence/cache-cost gates can
reject an approved classification. `--pruning-experiment-set final-validation`
passes the new cases through the production policy after adding two completed
terminal checks to reach the default four-call cadence. Its CSV also checks the
original required outputs and the newer required result bodies for loss.

```sh
# Original default confidence, physical policy, same new recorded request cuts.
go run ./cmd/uah compaction eval --pruning-experiment \
  --pruning-experiment-set final-validation \
  --pruning-schema necessity-guarded-v1 \
  --openrouter-decision-model baseten/elephant-1 > guarded-policy.csv

# Explicit alternative cutoff selected before second validation.
go run ./cmd/uah compaction eval --pruning-experiment \
  --pruning-experiment-set final-validation \
  --pruning-schema necessity-guarded-v1 --pruning-confidence 0.9 \
  --openrouter-decision-model baseten/elephant-1 > guarded-policy-09.csv
```

Use the complete measurements and caveats in
`reports/semantic-pruning/prompt-search/report.md`. No coding-model completion,
actual local prefill latency, or production task-retention claim follows from
these synthetic decisions. The feature remains disabled by default.
