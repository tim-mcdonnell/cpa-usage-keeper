# Coverage hardening and threshold tuning

Status: residual hardening implemented; real-history validation pending.
Baseline: `1950401ea9b76947a275737a2586690543ab4720`.
Authoritative parent design: `origin/quota-measurement-design:docs/design/confidence-aware-quota-estimation.md`.
Analysis date: 2026-07-24.

## Scope

This document records the slice-5 refinement to coverage detection, the evidence available for threshold decisions, and the retention decision.
Estimator behavior remains behind `estimate.Estimator`, with no API or database schema change.

## Evidence search and privacy boundary

The checkout contains no `data/app.db`.
No `app.db`, SQLite file, CPA Usage Keeper environment file, running CPA Usage Keeper container, or CPA Usage Keeper Docker volume was found in the accessible checkout, sibling Orca paths, standard user application-data paths, or the searched user-directory depth.
The repository and all reachable Git objects contain one quota-estimator corpus, `internal/quota/estimate/testdata/synthetic_capacity.golden.json`, and it is explicitly synthetic.
The fork has zero GitHub releases and zero retained GitHub Actions artifacts.
The available repository tags and workflow definitions contain source and binary packaging behavior, but no production-derived quota-observation aggregates.
The search therefore found zero accessible accrued-history databases and zero legitimate production-derived aggregate datasets.
This is not evidence that a deployment has zero quota-observation rows, because no deployment database was available to count.
No credential identifiers, account identifiers, event records, prompts, responses, tokens, or raw provider payloads were copied into the repository.

## Reproducible read-only analysis

`cmd/quota-observation-analysis` opens an existing SQLite file through the repository's `mode=ro` and `_query_only=on` reader.
It reports only database size, allocated bytes, total row volume, time range and ages, distinct credential and window counts, distinct reset-epoch count, active recording days, recent row counts, recording rates, null-versus-zero attributed-token counts, and aggregate estimator confidence, flag, point-class, sample, span, resolution, stability, and finite-interval-width distributions.
It does not emit credential identifiers, account identifiers, window identifiers, individual observations, event data, or provider payloads.
Its tests compare the database digest before and after analysis and cover both an aggregate fixture and a database without the observation table.

Run it against a stopped copy or backup of a real application database with:

```sh
go run ./cmd/quota-observation-analysis --db /absolute/path/to/app.db
```

Pass `--now` for a reproducible snapshot:

```sh
go run ./cmd/quota-observation-analysis \
  --db /absolute/path/to/app.db \
  --now 2026-07-24T12:00:00Z
```

The remaining input needed to complete real-history validation is one readable `app.db` copy containing genuinely accrued `quota_observations`, or the analyzer's complete JSON output from such a database together with its capture time.

## Residual coverage refinement

The original zero-coverage rule still runs first and remains model-free.
It catches a utilization increase of at least one detected resolution unit when attributed token spend does not increase.
The new residual pass considers only positive-spend intervals that survived the zero-coverage pass.
It calculates each interval's utilization-per-token rate and uses the median plus three normal-scaled median absolute deviations as a deterministic upper boundary for a clean candidate set.
The baseline slope is fitted through the origin from only those clean interval deltas.
At least four clean intervals and one additional evaluable interval are required before the residual pass can make a coverage claim.
An excluded interval is classified as residual contamination only when its positive utilization residual exceeds the maximum of 0.5 percentage points, twice the empirically detected percentage resolution, and three normal-scaled median absolute deviations of clean-fit residuals.
An isolated positive residual with an immediate compensating negative residual before or after it is left to the existing point-outlier policy instead of being mislabeled as bypass.
A consecutive run of positive residuals is treated as a coherent alternative slope and is also not classified as bypass, because the estimator cannot distinguish that pattern from an ordinary workload-mix regime change.
Every detected residual is accumulated as a percentage offset, the contaminated interval endpoint is classified `coverage_gap_interval` and excluded, and the capacity regression is recomputed from the retained adjusted points.
The suspect interval never participates in the clean baseline or the final fit.
The existing coverage confidence policy is unchanged: any detected gap caps confidence at `low`, and contamination above 30 percent of intervals suppresses the estimate.

The adversarial corpus proves that a nonzero-spend bypass missed by the old rule is found and that its clean capacity is recovered.
It also proves that bounded model noise stays clean, zero and null attribution remain distinct, pricing changes, mix shifts, and reset epochs compose with the refinement, transient outliers remain outliers, input permutation does not change output, bootstrap output is deterministic, and degenerate series do not create a coverage claim.
Assertions check exact classifications, offsets, flags, fit results, epoch isolation, cost-segment selection, and serialized output rather than only checking that a call returned.

## Concurrent-bypass limitation

The refinement detects isolated positive residual contamination while proxied traffic is present.
It cannot identify bypass traffic that remains proportionate to proxied traffic closely enough to form a coherent alternative slope within the robust residual guard.
The existing concurrent-bypass golden scenario therefore remains biased and unflagged by design.
The drill-down warning remains accurate and must not be weakened.

## Threshold decision

No existing confidence, reset, outlier, mix-shift, staleness, bootstrap, or contamination-suppression constant was changed.
Changing those constants without accrued history would invent empirical support that this checkout does not have.
The new residual thresholds are conservative structural guardrails required to implement the refinement, not claimed production tuning.
They combine a minimum absolute movement, detected provider resolution, and robust clean-series noise so ordinary quantization or model noise does not become a bypass claim.
The real-history threshold-tuning acceptance criterion remains unproven until the analyzer is run against genuinely accrued observations and the resulting aggregate evidence is reviewed.

## Retention decision

No quota-observation deletion is implemented in this slice.
With no accessible production database, there is no measured row volume, database size, age distribution, credential count, window count, or recording rate that could justify irreversible thinning.
Observation rows continue to be append-only and remain covered by the existing database backup behavior.
Retention must be reconsidered after at least 30 active recording days are available and whenever the aggregate analyzer reports either 1,000,000 quota-observation rows, 1 GiB of database allocation, or more than 365 days of history.
Any future implementation must remain repository-bounded, delete-only, deterministic, backup-compatible, and prove that retained recent-data estimates are byte-for-byte unchanged before and after thinning.
