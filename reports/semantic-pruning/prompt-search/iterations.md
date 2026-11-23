# Prompt-search log

Objective: increase correct pruning at the unchanged production thresholds
(confidence and chosen probability >= .98), while preserving required evidence.
Also inspect raw probabilities and action errors; no performance claim from
batched mutation screening. Production hard guards remain unchanged.

Starting point: binary-v1 has zero removals, retains even the exact duplicate,
and avoids the original prompt's known retention mistakes. Earlier held-out
cases are now development data.

Frozen before inference: six prompt files, one shared pair of option descriptions,
and twelve fresh validation cases (six discardable, six retain), including two
larger windows. Hashes are in frozen-before-screening.json. Labels are not sent
to inference. Selection uses development only; validation is run after selection.

Round 1 hypotheses:

- baseline: reproduce binary-v1 against the same evidence.
- concise: reduce asymmetric negative instructions and explain duplicate copies.
- counterfactual: ask explicitly whether required information disappears.
- checklist: give a precise decision order, starting with complete duplicates.
- examples: balance positive and negative contrastive examples.
- marginal: distinguish useful information from a necessary historical copy.

Screen all six in independent questions sharing the identical candidate state.
One screening repetition is exploratory, not a performance comparison. Confirm
promising mutations for five repetitions with baseline and then validate.

Round 1 result (15 development cases; 5 eligible positives, 3 eligible negatives):
Counterfactual recovered all 5 positive raw action judgments with 0 eligible
negative action errors. Mean prune probability on positives was 0.748; duplicate
0.815. Baseline got 1/5 positives, mean 0.384. No variant reached the production
threshold. Marginal also got 5/5 but wrongly discarded the flaky-test case;
reject it. Several mutations propose dropping protected skill/instruction cases;
the unchanged hard guards prevent application, but this motivates a guard-first
mutation. Noneligible parallel/incomplete cases are not used for model selection:
the classifier's candidate view does not include all earlier sibling calls.

Round 2: use counterfactual as parent. Test explicit protected-content priority,
a concise version, concrete contrasting examples, coverage-oriented option
labels, and a reversed "is the separate body needed?" question. The baseline and
parent remain controls. Option-description mutations are recorded in variants.json.
No validation cases have been queried.

Round 2 result: counterfactual remains 5/5 raw positives and 0/3 eligible
retention errors. Reversed necessity question also gets 5/5 and 0/3, with higher
mean prune probability (0.792), including duplicate 0.937. Guarded/contrast/yesno
increase positive confidence but each wrongly drops all 3 eligible retention
cases; reject them. Coverage gets 4/5; minimal gets 5/5 with one retention error.
Still no .98-threshold approvals.

Round 3: refine the necessity question with explicit protection and missing-fact
language. Compare unchanged native JSON evidence with a text rendering of exactly
the same messages/calls/results and chronology. This tests whether nested item
wrappers obscure the comparison. Labels and oracle remain outside both formats.
Re-evaluate controls with each format. Fresh validation remains unqueried.

Round 3 result: native necessity question remains strongest safe classifier:
5/5 positives, 0/3 eligible retention errors, mean positive probability 0.807,
duplicate 0.952. Plain text raises that to 0.852 but introduces two raw retention
errors (low probability, below any proposed policy threshold). Reject text as
primary presentation for now. Added guard/short variants introduce retention
errors; proof wording loses most positives. Keep the native necessity parent.

Round 4 final screen: compare the same necessity wording through native noul
P(true), plus coverage/loss boolean statements. This isolates whether the choice
labels/descriptions are suppressing the decision margin. Noul has no confidence;
for comparison ONLY derive binary-choice-equivalent confidence = abs(2p-1), so
production thresholds are not weakened. No validation cases queried yet.

Round 4 result: necessity-noul gets 5/5 eligible raw positives, 0/3 retention
errors, mean positive probability 0.834, duplicate 0.961. The positive coverage
boolean loses positives and makes one retention error. Loss boolean makes all
3 retention errors. Reject those. Keep baseline, counterfactual, reversed choice
and necessity-noul for confirmation.

Before confirmation, fix a reproducibility issue: json/v2 map ordering was not
canonical, including option descriptions. Canonicalize actual inference requests
and log their SHA256. Earlier screens are exploratory; repeat the controls under
this final harness. No validation feedback has been used. Confirm five times on
development, choose prompt and any separately labeled threshold calibration from
development only, then run the frozen validation once selection is locked.

Pre-validation protocol amendment (before querying any validation case): replace
literal target ID "old" with opaque call IDs uniformly in all twelve cases.
Expected decisions and evidence contents are unchanged. This avoids giving the
classifier the "old" cue and checks the selected prompt on realistic IDs.
The amendment and a new validation hash are recorded before validation inference.

Canonical five-repeat development confirmation: byte-identical SHA256 per case,
no request failures, deterministic returned probabilities. Baseline 0/25 eligible
positive actions and 0/15 negative errors. Necessity-noul 25/25 and 0/15, with
positive probabilities 0.7469–0.9476, negative maximum 0.4688. Both still approve
zero at the unchanged default .98 confidence. At a separately labeled .6
confidence (.8 effective prune probability), necessity approves 15/25 positives
and 0/15 negative errors, versus baseline 0/25 and 0/15. Lock necessity-noul,
native state, and experimental .6 threshold before validation. .98 remains the
production default. Selection is recorded in selection-before-validation.json.

Manual oracle audit before ANY validation inference: two comparison cases
accidentally repeated the original values in later patch messages, which would
make removal safe and contradict their intended retain oracle. Correct the
fixtures so whole-file rewrite calls supply only new state, leaving the needed
original behavior/value solely in the original output. Oracles are unchanged;
no model validation feedback was consulted. The final source hash is in the
selection record. Opaque call IDs are also retained. No further fixture or
prompt edits after this lock.

First validation FAILED retention at experimental .6. Selected necessity-noul
approves all six positive cases but also independent search (Pprune .889) and
original catalog value (Pprune .917), repeatedly. At .98 none apply. Standalone
development also shows a modest batch-composition effect near the cutoff (the
fixed-test probability shifts from .812 to .796). Reject .6 for rollout.
First validation is now explicitly development feedback, not held out.

Round 5: test missing-fact witness and strict necessity mutations emphasizing
single needed differences inside largely duplicated output. Lock a more
conservative experimental .9 confidence (Pprune >= .95) before screening, using
previous validation now only as calibration data. No production default change.
A second twelve-case balanced set with realistic unique Go/config/search content
and two larger catalog windows was frozen before this round; hash is in
final-set-freeze.json. Do not query it until final prompt selection is locked.

Round 5 result: missing-fact mutation removes all raw eligible retention errors
on augmented development (0/8), but recognizes only 6/11 positives. At .9 it
approves only two duplicates. Parent approves four positives and zero negatives
at .9, but still has three raw retention mistakes below threshold. Strict
necessity approves nothing at .9. No request failures, including large inputs.

Lock final policy BEFORE second validation: retain parent at .9 and add a
missing-fact veto (its Pprune must exceed .5 as well). This accepts three of the
four parent's development approvals: duplicate config, full search superset,
and duplicate long catalog. It blocks the intermediate source change. This is
a conservative conjunction, NOT a multiplied or calibrated combined probability.
Compare baseline, parent alone and this locked conjunction on the untouched
second set. Final selection and fixture/prompt hashes are recorded separately.
No more prompt or threshold mutations using second-validation outcomes.

Final independent validation: locked conjunction approves 3/6 positive targets
and 0/6 required targets, identically over five repeats. The same three clear
both .9 and .98 in that decision snapshot. Baseline approves zero at either gate.
No prompt or threshold was changed using these outcomes.

Exact production-policy follow-up (uniform extra terminal checks, unchanged
cache gates): .98 applies nothing because context shifts move long duplicate
Pprune to .982. At preselected .9, guarded necessity prunes the large duplicate,
saving 4,324 estimated tokens and losing no annotated required original/newer
bodies. Baseline saves none. The deterministic heuristic saves the same 4,324
without remote inference. .9 policy suite median ~14.28s versus heuristic ~13ms.
Keep .98 as default, allow the new schema for explicit shadow trials, reject
routine physical rollout. No further tuning on final validation.
