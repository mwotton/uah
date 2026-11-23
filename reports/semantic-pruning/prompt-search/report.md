# Manual prompt search — 2026-11-17

Raw run artifacts referenced below were archived locally under
`.scratch/semantic-pruning-artifacts/`; this branch retains the summary data
and the prompt bundles used by the documented reproduction commands.

We improved the decision prompt, but have not beaten the deterministic heuristic
once the actual pruning policy and remote inference cost are included. Keep the
feature disabled by default; the new guarded prompt is suitable for a local
shadow trial, not routine destructive context rewriting.

## What changed

Five mutation rounds tested 20 distinct prompt/interface variants, plus native
JSON versus a text rendering. `iterations.md` records the hypotheses, rejects,
validation failures and selection locks. Prompts and option descriptions remain
in their round directories for reproduction.

The useful shift was from “is this information useful?” to **“does the user
still need this separate original body, beyond later evidence?”** Reversing the
question and using native `noul` P(true) improved the margin. A second question
asks whether even one needed fact is absent from later evidence. It can veto
pruning even when most of a long result is duplicated.

The final opt-in schema is `necessity-guarded-v1`. It uses two bounded boolean
questions in one request, with no generated summary. The production prompts are:

- `internal/compaction/prompts/prune_necessity.md`
- `internal/compaction/prompts/prune_missing_fact.md`

P(false) on the first question becomes P(prune). The second question's P(true)
at least 0.5 forces retain. We preserve both probabilities and the veto in the
journal. We do not multiply the probabilities or claim a calibrated joint safety
estimate. The confidence value for native booleans is explicitly derived as
`abs(2*p - 1)`, matching binary-choice confidence for policy comparisons.

The default confidence remains 0.98 and the feature remains off. Experimental
0.9 confidence implies at least 0.95 primary prune probability. An earlier 0.6
setting failed validation and is rejected.

## Search and independent checks

The original 15 cases became development data. The first new validation set
exposed confident errors: dropping complementary search evidence and a needed
original catalog value. Those cases then became development feedback. We froze
a second balanced twelve-case set before the last mutation round, with realistic
unique config/source/search data, opaque call IDs and two larger catalog windows.
The final prompt, veto and cutoff were locked before querying that second set.
Fixture and prompt hashes are in `final-selection-before-validation.json`.
No mutation was selected using the second set's outcomes.

We also fixed request reproducibility: JSON map serialization now uses canonical
ordering, including option maps. Five confirmation repetitions had identical
request hashes and identical returned probabilities. Earlier variation could
include option/question ordering, so the noncanonical screens remain exploratory.
Some differences also remain between standalone and multi-question batches;
the exact production request was tested separately below.

On the second untouched set, five repeats each:

| Decision rule | Disposable outputs approved | Required outputs approved incorrectly |
| --- | ---: | ---: |
| Original binary-v1, confidence 0.9 | 0/6 | 0/6 |
| New necessity question, confidence 0.9 | 3/6 | 0/6 |
| New necessity + missing-fact veto, confidence 0.9 | 3/6 | 0/6 |

The same three selected snapshot approvals also clear default confidence 0.98.
They are the complete config duplicate, whole-repository search superset, and
complete long catalog duplicate. Five repeats verify stability; they are **six
independent retention cases, not thirty independent observations**.

This is not perfect model accuracy. Without thresholds, the necessity question
has four wrong action judgments on the six negative cases. The missing-fact
question still misses the needed original value in the long catalog example.
The cutoff/veto combination prevents execution of those mistakes here. It is
not a guarantee for arbitrary coding sessions.

Large-input checks now cover near the intended window: first validation's two
large batched requests used 15,946 and 16,055 actual service input tokens. Second
validation used 12,869 and 12,971. These counts include question schemas. Long
inputs exposed the original-value retention failure, so small-fixture success
alone would have been misleading.

## Actual production-policy rewrites

A classification approval is not a physical prune. We ran the production adapter,
eligibility, confidence, savings, cadence and cache-cost gates on the second set.
Two completed terminal checks were appended uniformly to reach the default
four-call cadence without adding replacement task facts. Required bodies include
both the original negative-case evidence and the newer result the task needs.
No prompts or cutoffs changed after the second-validation lock.

At default confidence 0.98, both old and new model policies physically prune
nothing. Adding the later terminal checks changes the long duplicate's primary
probability from roughly 0.992 to 0.982, below the effective 0.99 default gate.
The smaller duplicate/superset outputs also fail the 512-token savings floor.

At the preselected experimental confidence 0.9, five repetitions of the same
12-cut suite produce:

| Strategy | Retained estimated tokens/suite | Saved | Rebuilt suffix | Required bodies lost | Rewrite time median [range], ms |
| --- | ---: | ---: | ---: | ---: | ---: |
| None/default compaction below threshold | 27,708 | 0 | 0 | 0 | <0.01 |
| Original binary-v1, 0.9 | 27,708 | 0 | 0 | 0 | 14,255.799 [14,139.708–16,706.262] |
| Guarded necessity, 0.9 | 23,384 | 4,324 | 4,425 | 0 | 14,279.470 [14,250.479–14,637.961] |
| Deterministic duplicate heuristic | 23,384 | 4,324 | 4,425 | 0 | 13.433 [9.716–16.014] |

The new prompt finally produces a real context reduction where the old prompt
does not. But it removes only the large duplicate after all gates, exactly the
same output the cheap heuristic removes. It provides no additional space saving
over that heuristic on this workload, and remote inference adds about 14 seconds
per suite. The two model variants' latency ranges overlap; there is no measured
latency improvement between them.

These are offline rewrite times, including synchronous client inference and
request construction, excluding primary-model inference and durable journal
writes (the benchmark uses an in-memory pruner). The client currently opens a
fresh transport per classification, so WAN measurements include that connection
setup and are not an optimized local-serving latency claim. Separate runs were
not interleaved between schemas; treat small latency differences as inconclusive.

The 4,425 suffix tokens are an item-boundary estimate, not measured Strata cache
misses. At an illustrative 1,000 prefill tokens/s, rebuilding that suffix would
cost 4.425 seconds; that rate is uncalibrated. Actual Qwen prefill/cache behavior,
future reuse, re-fetches, repeated failures and coding completion remain untested.
No full-agent speedup is established.

## What was learned

- Separate **information still useful** from **this historical copy needed**.
- Native boolean questions worked better here than overlapping category labels.
- More confident prompts can be worse: several “guard-first” and example-heavy
  mutations discarded retention cases with higher confidence. They were rejected.
- Repeated text with one unique needed fact remains difficult, especially near
  16k. A missing-fact veto helps on some errors but is not independently reliable.
- Presentation and batch composition can move probabilities enough to cross a
  threshold. Test the exact adapter and rewrite path, not just a prompt snapshot.
- Unresolved failures, skills, instructions and incomplete operations still rely
  on unchanged hard guards. Invalid/noneligible model proposals are logged rather
  than credited as successful retention. String-based guards are conservative and
  can also reject ordinary source/log content, limiting coverage.
- Retaining all failures is not rewarded: resolved test/lint failures remain
  positive prune examples, though this conservative policy often keeps them.

The next useful experiment would improve the evidence representation—especially
explicit, deterministic differences/unique facts—rather than merely push the
prompt toward saying prune more often. A live local shadow trial should collect
real coding trajectories before physical rollout.

## Try and reproduce

The implementation supports the new schema without changing existing defaults:

```toml
[semantic_pruning]
enabled = true
shadow = true
decision_schema = "necessity-guarded-v1"
confidence = 0.9
endpoint = "http://127.0.0.1:4000/v1/systemone"
api_key_env = "LITELLM_API_KEY"
max_context_tokens = 12000
```

Use confidence 0.98 to retain the original default gate. Keep shadow enabled
until real-session retention and local latency are measured. The endpoint must
be running with its planned 16k allowance; no model service or GPU was started.

```sh
export GOCACHE="$PWD/.cache/go-build"
# OPENROUTER_API_KEY must be loaded through the existing credential mechanism.
go run ./cmd/uah compaction eval \
  --pruning-prompt-dir reports/semantic-pruning/prompt-search/final-comparison \
  --pruning-search-set final-validation --pruning-search-repeats 5 \
  --pruning-confidence 0.9 \
  --openrouter-decision-model baseten/elephant-1 > decisions.jsonl

go run ./cmd/uah compaction eval --pruning-experiment \
  --pruning-experiment-set final-validation \
  --pruning-schema necessity-guarded-v1 --pruning-confidence 0.9 \
  --openrouter-decision-model baseten/elephant-1 > rewrites.csv
```

Model responses identify `baseten/elephant-1-20261021`. Completed search and policy
runs used 752 requests with no service/schema failures, reporting approximately
$0.09246 in input charges. This excludes the preceding session's initial
experiment. Mutation and policy raw outputs are preserved; `search-usage.json`
and `policy-summary.json` aggregate them. No key was copied into this repository.

Passing checks: compaction/evaluation/config/app race tests, embedded and CLI
compaction/rewind/pruning regressions, canonical request/boolean/veto contract
tests, label-leak checks, frozen-fixture oracle checks and vet. The two previously
identified terminal-environment failures remain documented in the initial report.
No changes were pushed and no PR was opened.
