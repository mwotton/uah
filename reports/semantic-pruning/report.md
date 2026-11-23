# Elephant-guided pruning experiment — 2026-11-17

Raw run artifacts referenced below were archived locally under
`.scratch/semantic-pruning-artifacts/`; this branch retains the summary data
and the prompt bundles used by the documented reproduction commands.

Follow-up: five rounds of prompt mutation and new validation are recorded in
`reports/semantic-pruning/prompt-search/report.md`. The text below preserves the
initial experiment; use the follow-up for the newer guarded prompt.

**Recommendation: keep disabled for routine coding. Use local shadow mode first.**
The prototype works, but neither the original nor a revised binary Elephant
prompt improved on the deterministic control
at the conservative 0.98 thresholds. Its remote classification added latency
without removing any context. This is a negative result for this policy/model
configuration, not a measurement of a future local 3060 deployment.

## Implementation

The embedded compactor now runs an opt-in request-only pruning pass after its
ordinary rewrite. Raw transcripts and operation outputs remain unchanged.
It has local System One inference, conservative eligibility, deterministic
stubs, bounded verbatim failure lessons, durable evidence-bound decisions,
resume/rewind checks, shadow mode, and cache-aware physical batching.
Configuration and commands: `docs/design/semantic-pruning.md`.

Production endpoints are restricted to loopback. Only the explicit evaluation
flag enables OpenRouter, as authorized for this experiment. No weights were
loaded and no GPU allocation or service configuration was changed. The planned
local context is 16k; the prototype allows up to 14k estimated evidence tokens
and defaults to 12k to leave room for question schemas and tokenizer variance.

## Measurements

Baseline revision: `1eaf97d8214b7c2a56d66c01453c8a09f8783e04`.
Host: ram, 32 logical CPUs, Go 1.27.3. Initial load average: 3.18/2.71/2.48.
The normal Go cache symlink pointed at unavailable storage, so builds used the
ignored workspace cache. Other host workloads were not controlled; five rotating
repetitions were used. These are client-side elapsed times, not GPU kernel times.

The OpenRouter request used `baseten/elephant-1`; the actual response identified
`baseten/elephant-1-20261021`, provider `BaseTen`. The native Decisions endpoint
returned choices, probability distributions, confidence, usage and cost. No
prose generation or substitute chat model was used.

The following table sums all 12 independent synthetic cuts in one suite.
All strategies saw the same requests and 16,384-token window. Each individual
request fits, so the default strategy correctly does not compact yet. The
context numbers are estimates, not Qwen tokenizer measurements.

| Strategy | Retained tokens/suite | Saved tokens | Rebuilt suffix tokens | Rewrite time, median [range], ms |
| --- | ---: | ---: | ---: | ---: |
| None | 16,127 | 0 | 0 | 0.000 [0.000–0.001] |
| Existing default | 16,127 | 0 | 0 | 0.002 [0.001–0.005] |
| Eager identical-read heuristic | 15,379 | 748 | 820 | 5.984 [4.469–6.252] |
| Elephant, confidence/probability ≥0.98 | 16,127 | 0 | 0 | 7,515.244 [7,481.945–8,090.939] |

The heuristic pruned one duplicate read per suite. It rebuilt an estimated 820
tokens at the first rewrite. At an illustrative, **unmeasured** 1,000 prefill
tokens/s that is 820 ms of extra first-request prefill. Reusing the 748-token
saving across four later requests would save 2,992 logical input tokens, but
actual KV behavior and prefill timing could change that comparison. No end-to-end
agent speedup is established even for the heuristic.

Elephant made 55 successful requests across the five primary suites, processing
85,190 service-reported input tokens (about 1,549 per request, including the
question schema) and costing $0.00298165. It pruned nothing.
Its added classifier input per suite was 17,038 actual remote tokens, while
primary retained context stayed unchanged. Logical primary input and actual
classifier input are different tokenizers and are not added into a purported
actual total of Qwen work. The CSV records both. Prefill latency, cache hit
counts, repeated failed approaches, and task correctness remain unmeasured.

The original recorded fixture gives three cuts; the capture reproduced its
recorded compaction (1 of 1). Within this single captured comparison:

| Strategy | Median retained tokens | Median rebuilt suffix | Summary-call input estimate |
| --- | ---: | ---: | ---: |
| None | 13,942 | 0 | 0 |
| Existing UAH automatic | 12,763 | 3,353 | 11,048 |
| Eager heuristic | 13,942 | 0 | 0 |
| Eager Elephant | 13,942 | 0 | 0 |

These recorded cuts deliberately evaluate a compaction event, following the
existing harness. Summaries came from 3 recorded uses and 15 stub uses across
strategies; this is not a measurement of fresh summary generation latency or
quality. The original baseline retained 13,941 tokens for none and 12,764 for
UAH automatic. Later capture's system context differs by approximately a token;
comparisons above use the same capture within one run.

Existing fact/skill/user retention and re-fetch tables are preserved in
`recorded-openrouter.txt`. The eager strategies leave those fixture requests
unchanged, hence lose no additional measured facts and cause no additional
predicted re-fetch. This establishes no benefit. The default automatic strategy
has 11% predicted re-fetch over 18 later calls in this fixture.

## Accuracy and independent review

Six primary cases independently expect the old result to become disposable.
The heuristic removes one; Elephant removes zero, so retaining every historical
failure is **not** treated as a perfect outcome. Five eligible old interactions
were classified, repeatedly, as `superseded`, with confidence approximately
0.23–0.55. Even the exact duplicate read's top probability was around 0.59, far
below the configured threshold. The four overlapping categories appear to spread
probability; their calibration needs investigation before changing the policy.

Three additional held-out retention cases were written after freezing the
prompt and thresholds. No model was shown their expected answers.

| Held-out case | Raw old-result classification over five runs | Manual judgment | Physically pruned |
| --- | --- | --- | ---: |
| Original and changed file needed for comparison | active/retain | retain | 0 |
| Flaky test passes on rerun without a demonstrated fix | superseded, 5/5 | retain | 0 |
| Different search in a different directory | superseded, 5/5 | retain | 0 |

Thus two of three held-out raw action judgments were wrong on every repeat.
Their confidence was low (roughly 0.14–0.28), and the production gates prevented
loss. These are classification errors, not executed pruning errors. Lowering
thresholds is not a justified cure for the lack of pruning. At the current
minimum configurable confidence of 0.5, these particular mistakes still fail
the confidence gate.

These are small-context classification cases, not a 16k context-pressure test.
The configured 12k estimate is an upper allowance, not the actual input size.

The held-out run made 25 successful inference requests, processing 41,470 input
tokens (about 1,659 per request) and costing $0.00145145. The final measured synthetic and held-out runs
therefore cost $0.00443310 in service-reported inference charges. Exploratory
runs and schema probes are additional and are not included in that subtotal.

Eligibility guards deliberately exclude skills, obvious instructions, negative
searches, incomplete operations, parallel siblings and unresolved failures.
`unclassified` rows are guard outcomes, not evidence of model accuracy. The
corrected-command case remains protected because its failed invocation differs
from the successful invocation. The genuine test-fix case includes a patch and
an identical successful rerun, so it can reach the classifier. A fake classifier
in unit tests proves that an approved resolved failure becomes a compact output
with its original failure lesson retained.

## Prompt revision requested during the experiment

The original categories overlap: active/retain both keep the output, while
resolved/superseded both permit replacement. We added an independently selectable
`binary-v1` prompt. It names the candidate ID, describes the exact replacement,
requires concrete later evidence, and explicitly covers before/after comparisons,
disjoint searches and passing-but-unfixed flaky tests. Full prompt:
`internal/compaction/prompts/prune_binary.md`.

It was evaluated on the same cases, with five rotating repetitions and unchanged
thresholds. The held-out cases now serve as a prompt-development set; they are
no longer an untouched final test set for the revised prompt. More unseen cases
would be required before any accuracy claim about that prompt generalizes.

| Prompt | Primary physical prunes/suite | Raw held-out action errors | Primary suite latency, median [range], ms |
| --- | ---: | ---: | ---: |
| Four-label lifetime-v1 | 0 | 2 of 3 cases, all repeats | 7,515.244 [7,481.945–8,090.939] |
| Binary-v1 | 0 | 0 of 3 cases, all repeats | 7,658.659 [7,394.623–8,036.371] |

Binary-v1 chose retain for every eligible original target, including the exact
duplicate read (P(retain) 0.526266). Its held-out retention probabilities were
approximately 0.77–0.85. It is clearer and safer on these known pitfalls, but too
conservative to provide savings here. This change is not a measured performance
improvement; latency ranges overlap and neither policy changes Qwen context.

Reported confidence is peak probability rescaled above uniform, so confidence
values across two- and four-choice questions are not directly comparable. Both
runs also require selected probability >=0.98; the confidence gate raises the
effective peak threshold to 0.985 for four choices and 0.99 for two.

The binary primary run processed 96,740 input tokens over 55 requests, costing
$0.00338590. Its held-out run processed 46,720 over 25, costing $0.00163520.
That is about 1,759 and 1,869 actual input tokens/request respectively, including
the longer prompt/schema. These inputs still do not approach a 16k stress test.
Select the revised prompt with `--pruning-schema binary-v1` in the benchmark,
or `decision_schema = "binary-v1"` in the local configuration. It remains opt-in.

## Example contexts

The duplicate-read fixture contains 3,080 bytes of identical output in each read.
Abbreviated before:

```text
call old: Read source.go
result old: row: alpha beta gamma delta [repeated 110 lines]
call new: Read source.go
result new: row: alpha beta gamma delta [repeated 110 lines]
...two later completed checks...
```

The heuristic's measured after-context preserves both call/result pairs:

```text
call old: Read source.go
result old: [uah elided this output to save context: Read, 771 tokens.
            Run it again if you need it.]
call new: Read source.go
result new: row: alpha beta gamma delta [all 110 lines retained]
...two later completed checks...
```

That cut goes from 1,594 to 846 estimated tokens. Elephant's actual after-context
at 0.98 is identical to before. The approved-failure regression test illustrates
a separate deterministic replacement, **not an observed remote pruning success**:

```text
call a: Bash {"command":"go test ./..."}
result a: [uah elided ... Bash "go test ./...", exit 1 ...]
          Lesson from original failure (verbatim excerpt):
          FAIL TestValue: want 2 got 1
          [bounded original head/tail, including original exit metadata]
call b: Bash {"command":"go test ./..."}
result b: ok example/demo
          Exit code: 0
```

## Tests and limits

Passing: compaction/evaluation/config race tests, app/config tests, embedded
pruning and unavailable-service integration tests, compaction/clear/rewind
regressions, and `go vet` on affected packages. Tests cover model consumption,
parallel/incomplete tools, protected content, confidence/probability gates,
budget/cadence gates, read-only raw history, persistence failure, malformed HTTP
answers, timeouts, replay, rewind/branch mismatch, and deferred batch commits.

Two broader tests fail in this terminal environment: `TestBoxes_FailClosed`
(granting a temporary path already under a writable root) and
`TestPrintExitColors` (256-color versus truecolor escape expectations). Both
failures were reproduced against an untouched archive of the baseline revision;
see `preexisting-failures.txt`. They are not new pruning regressions.

No Strata/Qwen service listened on 127.0.0.1:18080; the local Elephant route was
also absent, and Docker was unavailable. No live coding tasks were run. Actual
Qwen prefill, KV prefix preservation, 3060 PCIe x1 effects, subsequent re-fetch,
repeated failed approaches and task completion/correctness are untested. The
current fact metric is a path/basename presence proxy; tool arguments can retain
a path while losing its contents. Re-fetch is a correlation proxy. Their
limitations are documented in the design document.

Physical pruning remains experimental: keyword instruction guards are not a
proof that arbitrary instruction-bearing text will be recognized, and an
identical successful rerun can be flaky. A separate recovery tool, asynchronous
classification, latency-calibrated thresholds and a streaming replay benchmark
are future work. The offline cuts measure one rewrite, not a cumulative cache
simulation over an actual model trajectory.

Keep the normal defaults. For the upcoming local service, start with shadow
mode with `decision_schema = "binary-v1"`, 0.98 confidence/probability, 12k context allowance, 512-token minimum savings,
four-call batching and a 0.25 savings/suffix ratio. Measure actual prefill first,
then replace the illustrative 1,000 tokens/s estimate. There is currently no
measured reason to enable Elephant-guided physical pruning routinely.

## Artifacts and reproduction

`docs/design/semantic-pruning.md` has complete commands and configuration.
`baseline.txt` and `baseline-tests.txt` preserve the initial existing harness.
`openrouter.csv`, `heldout.csv`, `recorded-openrouter.txt` and `aggregates.json`
are the original-prompt final measurements. `binary.csv` and
`binary-heldout.csv` hold the prompt-revision measurements. `openrouter-schema-probe.csv` preserves the initial
conservative rejection of OpenRouter's dated response model identifier; the
adapter now accepts a dated version of the requested remote alias.
`openrouter-prebudget.csv` is an earlier run before correcting the synthetic
harness to invoke default compaction only when its budget is reached; it is
excluded from the final comparison. `synthetic.csv` is the initial local-service
unavailable/fallback run, also excluded. Test logs are supporting records.
No key was written to this repo. No changes were pushed and no PR was opened.
