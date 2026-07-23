# Confidence-aware subscription quota estimation

Status: decision-complete, ready for spec authoring; no implementation yet.
Baseline commit: `a6e1ed6f820e7ef88aa8ac0e21785ba17e7a474a`.
Related research: `~/orca/projects/quota-measurement/research/cliproxyapi-central-quota-observability.md` and `~/orca/projects/quota-measurement/research/codex-5h-reset-semantics.md`.
This revision incorporates two adversarial design reviews (Fable 5 fork and gpt-5.5), the decisions from the 2026-07-23 grilling session (section 11), and the resolutions of the gpt-5.6-sol adversarial spec review (section 12).

## 1. Problem

Usage Keeper today shows provider quota utilization and an estimated full-window cost computed from a single point.
The frontend divides current window spend by the current used fraction in `quotaWindowUsageEstimate` (`web/src/components/usage/credentials/credentialViewModels.ts:269`).
That estimate is recomputed from whatever the latest quota reading happens to be, carries no history, and presents every value as equally trustworthy.

This proposal adds historical multi-point estimation.
The system persists quota observations over time, relates quota movement to API-equivalent spend recorded from CLIProxyAPI traffic, fits a regression per quota window instance, and reports capacity estimates with explicit confidence.
It also makes visible when account quota moved without corresponding proxied traffic, which indicates usage that bypassed CLIProxyAPI.

## 2. Current-state code-path map

### 2.1 Quota readings

Quota state is held only in memory today, in `Service.refreshTasks map[string]*RefreshTaskRecord` keyed by auth_index (`internal/quota/service.go:33`, record type `internal/quota/refresh.go:89`).
There is no quota table.
A restart loses all quota history.

Two paths produce quota readings, and both converge on the same normalized shape before caching:

1. Provider poll.
   Manual refresh (`POST /quota/refresh`, `internal/quota/refresh.go:146`), scheduled auto-refresh (`internal/quota/auto_refresh.go:39`), and inspection rounds (`internal/quota/inspection.go:57`) enqueue per-credential tasks.
   Each task runs `Service.Check` (`internal/quota/service.go:214`), which resolves a provider handler (`Service.resolveQuotaHandler`, `internal/quota/service.go:249`, registry `internal/quota/registry.go`) and calls the account quota endpoint through CLIProxyAPI's Management API (`ManagementAPICaller`, `internal/quota/types.go:10`, implemented in `internal/cpa/client.go:458`).
   Codex uses `https://chatgpt.com/backend-api/wham/usage` and Claude uses `https://api.anthropic.com/api/oauth/usage` plus `/api/oauth/profile` (`internal/quota/config.go:86-128`).
   Raw payloads are normalized into `[]QuotaRow` (`internal/quota/normalize.go:21`, row type `internal/quota/types.go:47`).
   Normalization is not a pass-through: some providers report a percent directly, others report used/limit or a remaining fraction from which a percent is derived (for example Kimi at `internal/quota/normalize.go:383`), and reset timestamps arrive as epoch seconds (Codex) or ISO strings (Claude).
   The Codex primary/secondary distinction is a presentation role, not a stable identity: the weekly window occupies the primary slot when no 5-hour window is present, and the row key encodes the role (`internal/quota/normalize.go:143-149`).

2. Codex response headers.
   Proxied usage events carry upstream response headers.
   `BuildUsageHeaderSnapshot` (`internal/quota/header_snapshot.go:39`) filters `X-Codex-*` quota headers, `parseCodexHeaderQuota` (`internal/quota/codex_header.go:13`) parses them (`Used-Percent` is an arbitrary float, not necessarily an integer), and an async worker merges the result into the in-memory cache (`internal/quota/header_cache_worker.go:197`).
   Header readings are coalesced at three points before reaching that worker: the sync pipeline keeps only the newest snapshot per credential per committed batch (`coalesceUsageHeaderSnapshotsByAuthIndex`, `internal/service/sync.go:392-413`), the aggregation runner keeps one pending snapshot per credential (`internal/poller/usage_aggregation_runner.go:91-163`), and the worker itself batches on a roughly one-minute flush interval.
   The worker also rejects snapshots older than the current cache (`shouldProcessUsageHeaderQuotaSnapshot`, `internal/quota/header_cache_worker.go:404-424`), and its cache merge is field-level against the previously cached record (`mergeUsageHeaderQuotaRow`, `internal/quota/header_cache_worker.go:427`), so the cached row after a header update can combine fresh header fields with stale poll fields.
   Any observation-recording contract is therefore a sampled contract, not an every-reading contract; the surviving snapshot per coalescing interval is the recordable unit.

Both paths call `attachWindowUsageStats` before caching (`internal/quota/refresh.go:410-412`, `internal/quota/header_cache_worker.go:393`).
That function computes the window time range from `reset_at` and `window.seconds` (`quotaRowUsageWindow`, `internal/quota/usage_stats.go:106`) and backfills `WindowUsageTokens` and `WindowUsageCost` from local `usage_events`.
Critically, it only backfills when the provider did not already supply both values (`shouldBackfillWindowUsageStats`, `internal/quota/usage_stats.go:86`; preservation covered by `TestCodexProviderPreservesProWindowUsageFields`, `internal/quota/test/codex_test.go:247`).
So `WindowUsageTokens/Cost` on a cached row is sometimes local proxied spend and sometimes provider-reported account-wide usage; the two are not interchangeable, and the design below never conflates them.

### 2.2 Usage events and spend

Usage events arrive from CLIProxyAPI's Redis-compatible queue, land raw in `redis_usage_inboxes`, and are processed into the `usage_events` table (`internal/entities/usage_event.go`, pipeline in `internal/service/sync.go:198`).
`usage_events` is insert-only in normal operation, with an opt-in 90-day retention delete (`internal/repository/db.go:338-372`).
Events carry a textual `auth_type` (`oauth`/`apikey`) plus `auth_index`; identities live in `usage_identities` with an integer `auth_type` enum and a unique `(auth_type, identity)` key (`internal/entities/usage_identity.go:18`), so joins go through an explicit enum-to-text mapping (`internal/repository/usage_identities.go:547`).
Events carry no account or credential-incarnation identifier; identity sync updates the `usage_identities` row in place on credential replacement (`internal/repository/usage_identities.go:724`).
The existing window spend query filters by `auth_index` only, relying on index uniqueness across auth types (`internal/repository/usage_window_stats.go:221`).

Cost is never stored.
It is computed at query time from stored token buckets against the live pricing snapshot (`pricing.Catalog`, `internal/pricing/catalog.go`; resolver `internal/pricing/resolver.go:41`).
There is no price versioning and no snapshot fingerprint today: after a price change, every historical query reprices old tokens at the new price.
Models without a price entry contribute tokens but zero cost, silently (`usageWindowStatsFromTokenStats` ignores `CostResult.Available`, `internal/repository/usage_window_stats.go:326`).
Token totals summed across events are heterogeneous: different models, token buckets, and service tiers can carry different effective quota weights, so a raw token sum is a workload-mix-dependent quantity, not a provider-defined unit.

### 2.3 Persistence conventions

SQLite via GORM, single writer pool plus a read-only reader pool routed by dbresolver (`internal/repository/db.go:34-69`).
Migrations are dated Go files registered in an explicit ordered slice (`internal/repository/migration/migration.go:120`), with `AutoMigrate` plus `MarkAllAsApplied` on fresh databases.
The migration order and the entity registry are each locked by a hardcoded-list test (`internal/repository/migration/migration_test.go:17`, `internal/entities/test/entities_test.go:14`).
Adding a table means: entity in `internal/entities/`, registration in `entities.All()`, a dated migration plus ordered entry, updates to both registry tests, and free functions in a concern-named file under `internal/repository/`.

### 2.4 API and UI

Quota endpoints live in `internal/api/quota.go` under `/api/v1/quota/*`, admin-only.
Responses largely expose quota-package types through the `QuotaProvider` interface (`internal/api/router.go:31`) rather than api-package DTOs; the batch cache read is `POST /quota/cache` taking `auth_indexes` (`internal/quota/refresh.go:38`).
The Credentials UI is the `auth-files` tab of `UsagePage`, fed by `useCredentialsTabData.ts`, which pages auth indexes and batch-loads the quota cache (`web/src/components/usage/credentials/useCredentialsTabData.ts:76`).
`QuotaBar` renders per-window bars with a current-versus-estimated toggle (`AuthFileCredentialsSection.tsx:1853`, toggle at `:1522`); view-model logic is in `credentialViewModels.ts`.
Charts use Chart.js v4 via react-chartjs-2, centrally registered in `web/src/lib/chartjs.ts`.

## 3. Domain definitions and invariants

Quota observation.
One surviving reading of one provider quota row for one credential at one instant, as produced by one source, captured before any cache merging or cache-staleness rejection.
Fields: credential identity, window kind identity, observation time, source, the raw utilization values with provenance, the raw and normalized reset metadata, provider-reported window usage when present, and locally attributed spend with its composition.
Observations are append-only facts: never updated, recomputed, or backfilled.
Recording is a sampled contract (section 2.1); the write policy in 5.2 defines which readings become rows.

Quota window kind.
The class of window a quota row describes, identified by a canonical, role-independent `window_kind_id` computed from provider, metered feature or scope, the provider's stable limit identifier where one exists, and window duration.
The Codex primary/secondary role labels and the raw `QuotaRow.Key`/`GroupKey` are stored as provenance but never participate in identity, because the same physical weekly window changes role depending on which windows are present.
Example: Codex main-bucket 5-hour and main-bucket weekly are distinct window kinds on the same credential regardless of which slot they occupy.

Estimable window.
A window kind on an explicit allowlist whose metered traffic is the credential's overall proxied traffic: Claude `five_hour` and `seven_day` overall windows and the Codex main-bucket 5-hour (18000s) and weekly (604800s) rate limits.
Feature-scoped quotas (Claude `seven_day_cowork`, Codex `code_review`, Spark and other additional buckets) meter a subset of traffic that local events cannot isolate; regressing total spend against them yields confident-looking garbage, so they are stored but never estimated, permanently (decision 6).
Attribution is computed only for estimable windows; non-estimable rows store null attribution, and null means not computed while zero means computed with no traffic.

Reset epoch.
One concrete instance of a quota window on one credential: the period ending at one provider reset boundary.
Epoch assignment is derived at read time from stored raw reset fields (see 6.2 for the deterministic clustering rule), so the policy can improve without touching historical rows.

Credential incarnation.
The pairing of an auth_index with one provider account and plan.
CPA can replace the credential behind an auth_index, and identity sync updates the identity row in place, so observations snapshot `account_id` and `plan_type` at capture time.
A change in either forces a series break, and because usage events carry no incarnation identifier, attributed spend within a window that spans the change cannot be separated; estimation for the new incarnation is therefore suppressed until the first natural reset boundary after the change.
Claude identities currently expose no account or plan discriminator in quota rows (`internal/quota/normalize.go:71-114` sets no plan type); where no discriminator exists, incarnation changes are undetectable, and this limitation is explicit: such series carry an `identity_unverified` note in diagnostics rather than a false guarantee.

Attributed spend.
The cumulative proxied usage of `usage_events` for the credential within the window up to the observation instant, computed by the observation recorder itself, unconditionally for estimable windows, into dedicated `attributed_*` fields: total tokens, the four canonical token buckets (input, output, cache read, cache creation), and API-equivalent cost.
The window bound is half-open `[reset_at - window_seconds, observed_at)`; on the header path the triggering usage event is additionally included by its event key, because HTTP rate-limit response headers conventionally describe post-request state, and the triggering event is being persisted in the same pipeline that produces the observation.
Attributed spend is never taken from `QuotaRow.WindowUsageTokens/Cost`, because those may be provider-reported account-wide usage (section 2.1).
Provider-reported window usage, when present, is stored separately and never used as regression input.
Attributed cost is priced with the pricing snapshot live at observation time and frozen; attributed tokens are exact and price-independent.
Freezing cost is deliberate: query-time repricing would silently shift regression inputs after any price change, and reproducibility of estimates from the observation table alone is a hard requirement.

Token capacity semantics.
A raw token sum is workload-mix-dependent: models, token buckets, and tiers carry different effective quota weights, and no provider contract establishes linearity of subscription utilization in unweighted tokens.
The token-denominated estimand is therefore explicitly "proxied token capacity at this credential's observed workload mix", never "provider capacity" in the abstract.
The stored bucket composition lets estimation detect mix shifts within an epoch (a `mix_shift` flag caps confidence) and preserves the raw material for constructing a weighted usage unit later without recapturing history.

Pricing snapshot identity.
A canonical content hash of the compiled pricing snapshot, recorded on each observation.
The hash payload is defined field by field over cost-affecting values only, in canonical order: for each model sorted by name, the tuple (model, pricing style, prompt price, completion price, cache-read price, cache-write price, model multiplier); for each rule sorted by (key, value), the tuple (key, value, multiplier).
Database ids and timestamps are excluded; reordering semantically identical rules must not change the hash, and changing pricing style must.
This is new pricing-package work: no fingerprint exists today, and the hash is computed once per compiled snapshot and carried on the resolver, so it always matches the prices that computed a given cost.

Coverage gap.
An interval between two consecutive observations in the same epoch where reported utilization increased while attributed spend did not increase at all.
This detects zero-coverage bypass (quota movement with no proxied traffic in the interval).
Bypass traffic concurrent with proxied traffic is not detectable by this rule and is a documented v1 limitation (section 6.4); estimates from epochs with detected coverage gaps are capped below the UI cutover.
Coverage gaps are derived at estimation time, never stored.

Estimate.
A derived fit over the observations of one epoch of one estimable window, producing capacity quantities plus quality measures and per-observation diagnostics.
Estimates are recomputable and safe to delete.

Confidence.
A grade attached to every estimate, computed from effective sample count, observed percentage span, uncertainty width, fit stability, pricing purity, and coverage gaps.
Low-confidence estimates are shown as such or suppressed, never silently presented as trustworthy.

Invariants:

1. Observations are append-only; no code path updates or deletes them except an explicit future retention policy, which may delete but never rewrite.
2. An estimate never mixes observations across credentials, credential incarnations, providers, window kinds, or reset epochs.
3. Observations preserve raw provider values (all utilization fields as reported, raw reset representation verbatim) alongside any normalization, so later parser or policy fixes can recompute derived quantities from stored rows.
4. Everything derived (epoch assignment, resolution detection, coverage flags, estimates, confidence, diagnostics) is recomputable from raw observations plus code; nothing derived is inferred once at write time and then trusted.
5. Regression x-axis values are locally attributed spend only; provider-reported window usage is display-only provenance.
6. No prompts, responses, raw credential JSON, or provider tokens are persisted.

## 4. Design options

### Option A: persist raw observations, derive estimates on read (recommended)

A new append-only `quota_observations` table written by an asynchronous recorder fed from the two producer seams.
A new pure estimation module fits regressions on demand when the API is asked for estimates.
No stored estimates.

Strengths: smallest new machinery; derived data cannot go stale; estimation policy can change freely; consistent with the repository's query-time-cost philosophy.
Weakness: estimate latency on read, negligible at this scale (an epoch holds at most a few hundred observations under the write policy in 5.2).

### Option B: persist observations plus a materialized estimates table

Adds a checkpointed worker maintaining `quota_capacity_estimates`, following the overview/activity rollup pattern.
Rejected for now: a second copy of truth that can lag or diverge, plus checkpoint and rebuild burden, for no benefit at this input volume.
The estimation interface leaves room to add caching later without API change.

### Option C: no new table; reconstruct history from `usage_events` and the in-memory cache

Rejected outright.
The quota cache is volatile, provider utilization includes bypass traffic that `usage_events` cannot reconstruct, and durable multi-point history is the core requirement.

## 5. Schema, write path, and migration strategy

### 5.1 Table

One new entity `QuotaObservation`, table `quota_observations`:

| Column | Type | Notes |
|---|---|---|
| id | int64 PK | |
| usage_identity_id | int64 | reference to `usage_identities.id` |
| auth_type | text | `oauth`/`apikey`, same domain as `usage_events.auth_type` |
| auth_index | text | credential identity string, as used by `usage_events` |
| account_id | text nullable | incarnation snapshot; null when the provider exposes none |
| plan_type | text nullable | incarnation snapshot |
| provider | text | normalized provider type |
| window_kind_id | text | canonical role-independent window identity (section 3) |
| quota_key | text | raw `QuotaRow.Key`, provenance only |
| scope | text | raw `QuotaRow.Scope`, provenance only |
| group_key | text | raw `QuotaRow.GroupKey`, provenance only |
| window_seconds | int64 nullable | as reported |
| observed_at | storageTime | captured once per reading (section 5.2) |
| source | text | existing `RefreshSource` values: `manual`, `scheduled`, `inspection`, `usage_header` |
| used_percent | float nullable | as reported or derived; provenance below |
| percent_source | text | `reported`, `from_remaining_fraction`, `from_used_limit` |
| remaining_fraction | float nullable | as reported |
| used | float nullable | as reported |
| limit_value | float nullable | as reported |
| remaining | float nullable | as reported |
| reset_at | storageTime nullable | normalized; null on parse failure |
| reset_raw | text nullable | the provider's reset representation verbatim |
| reset_after_seconds | int64 nullable | as reported |
| provider_window_tokens | int64 nullable | provider-reported window usage, display provenance only |
| provider_window_cost | float nullable | provider-reported, display provenance only |
| attributed_tokens | int64 nullable | estimable windows only; null = not computed |
| attributed_input_tokens | int64 nullable | composition, same scope |
| attributed_output_tokens | int64 nullable | composition |
| attributed_cache_read_tokens | int64 nullable | composition |
| attributed_cache_creation_tokens | int64 nullable | composition |
| attributed_cost_usd | float nullable | frozen at capture |
| attributed_cost_complete | bool | false when any tokens in the window had no price entry |
| triggering_event_key | text nullable | header path: the included triggering usage event |
| pricing_snapshot_hash | text | hash of the snapshot that priced attributed cost |
| created_at | datetime | |

Utilization resolution is not stored: it is an empirical property of a series, detected at read time (invariant 4); a single reading cannot establish it.
Indexes: `(usage_identity_id, window_kind_id, observed_at)` for series reads and `(observed_at)` for retention.
No unique constraint; the table is an event log, and write discipline prevents duplicates.

### 5.2 Write path and recording policy

A recorder component inside the quota package owns all writes and runs asynchronously: producers construct an immutable `QuotaReading` value (identity snapshot, resolved provider, source, `observed_at`, the raw normalized rows, and on the header path the triggering event key) and enqueue it on a bounded in-memory queue; a single recorder goroutine consumes it.
Producers never wait on the recorder or the database; if the queue is full the oldest entry is dropped and a drop counter is logged.
`observed_at` is captured exactly once, immediately after the provider response (poll path) or from the triggering usage event's timestamp (header path), and that same value is used for attribution, the observation row, and any cache timestamping, eliminating time-of-check races.

Producer seams:

- Poll path: at refresh task completion (`internal/quota/refresh.go:410-412` region), from the freshly normalized response before cache mutation.
- Header path: in the header worker before both the cache-staleness rejection and the cache merge, from the rows actually present in the header snapshot, so stale poll fields merged into the cache never masquerade as header observations and stale header readings are still recorded (they are excluded at read time by the quarantine rule, 6.2).

Recording decision, ordered cheap-first so attribution cost is only paid for candidates that pass:

1. Cheap gates from the in-memory last-recorded cache and one indexed max-event-id lookup: record when the reading is the first for its (usage_identity_id, window_kind_id) since process start, when its reset boundary changed, when `used_percent` changed, when new usage events exist for the credential since the last recorded row, or when a 30-minute heartbeat elapsed; otherwise skip.
2. Minimum spacing of 5 minutes per window kind, except for reset-boundary changes.
3. Only then compute attribution (estimable windows) and run the decision-confirm-and-insert in one writer transaction (read latest row for the key, compare, insert), preventing duplicates from cold-cache restarts.

The attribution query runs over raw `usage_events` filtered by `auth_type = 'oauth'` and `auth_index` with the half-open bound plus the triggering event, returning bucket sums, total, cost, and cost completeness against the resolver's snapshot; it deliberately avoids hourly rollups so aggregation lag cannot freeze an undercount.
The 5-minute spacing yields roughly 288 rows per window kind per day in normal operation; this is a normal-operation bound, not a hard ceiling, because reset-boundary changes and restarts add rows.
An absolute safety cap of 400 rows per window kind per day is enforced by the recorder with a logged refusal, so no failure mode can bloat the table.
Repeated identical percents at growing spend are kept deliberately: flat segments carry information.
Observation insert failure is logged and never blocks quota caching; no retry, since the next reading arrives shortly.
A load test exercising concurrent usage ingestion, poll refreshes, and header traffic against the single-writer database is part of slice 1 acceptance.

Slice 1 ships the observation series listing endpoint (7.1) as its verification surface, so recorded observations are auditable from day one through a stable admin-only contract rather than a transient log line.

### 5.3 Migration

One dated migration creating the table via `tx.AutoMigrate(&entities.QuotaObservation{})`, with: version constant and `orderedMigrations()` entry, entity added to `entities.All()`, and updates to both hardcoded registry tests (`migration_test.go:17`, `entities_test.go:14`).
Verification: fresh-database schema and index shape, existing-database upgrade, idempotent re-run.
Retention: none initially; a thin-out policy beyond 180 days is a later slice.
The existing SQLite online backup covers the table automatically.

## 6. Estimation module

### 6.1 Module shape

One deep module, package `internal/quota/estimate`, no I/O, all epoch, regression, outlier, confidence, and diagnostic logic inside:

```go
type Estimator interface {
    // EstimateWindows groups one credential's observations into
    // (window kind, reset epoch) series and fits each estimable series.
    EstimateWindows(obs []entities.QuotaObservation, now time.Time) []WindowEstimate
}

type WindowEstimate struct {
    Provider         string
    WindowKindID     string
    WindowSeconds    int64
    EpochResetAt     time.Time
    SampleCount      int
    EffectiveSamples int     // distinct (percent, spend) change points
    PercentSpan      float64
    Slope            *float64 // fitted, percent per token
    Intercept        *float64 // fitted, percent at zero attributed spend
    // Token-denominated results (primary; workload-mix-specific, see section 3).
    MarginalTokensPer100 *int64
    TokensAt100          *int64
    TokensCI95           *Interval // block-bootstrap interval on the delta series
    // Cost-denominated results (segment-scoped, see 6.3).
    MarginalCostPer100 *float64
    CostAt100          *float64
    CostCI95           *Interval
    CostSegment        *SegmentRef // hash + time range the cost fit used
    RSquared           *float64    // diagnostic only, not a gate
    Confidence         Confidence  // high | medium | low | insufficient
    Flags              []Flag
    Points             []PointDiagnostic // per-observation classification
    Method             string
}

type PointDiagnostic struct {
    ObservationID int64
    Class         string // included | outlier | coverage_gap_interval |
                         // stale_quarantined | pricing_excluded | pre_break
}
```

`Points` carries the per-observation classifications the drill-down UI renders; the frontend never reimplements estimator logic.
Handlers load observations through the repository and pass them in; the estimator is testable with golden datasets and swappable without touching handlers or UI.

### 6.2 Epoch assignment

Canonical reset time per observation: normalized `reset_at` when present, else `observed_at + reset_after_seconds`; observations with neither are stored but excluded from estimation.
Clustering is deterministic and anchored, not pairwise: observations are processed in `observed_at` order, the first opens an epoch whose anchor is its canonical reset time, and each subsequent observation joins the current epoch when its canonical reset time is within tolerance of the anchor, else opens a new epoch.
Tolerance is provenance-dependent, per the observed jitter in captured data: a few seconds (default 10s) when both reset times are absolute `reset_at` values, and `max(120s, 0.5% of window_seconds)` capped at 30 minutes when either side is derived from `reset_after_seconds`, whose jitter includes network and polling offset.
Forced series breaks regardless of reset time: `used_percent` drops materially (manual reset credits, provider-side reset), `plan_type` changes, or `account_id` changes.
After an incarnation break (plan or account change), estimation for the new series is suppressed until the first natural reset boundary after the break, because attributed spend within the spanning window cannot be separated between incarnations (section 3).
Mixed absolute and derived reset times within one epoch are permitted but flagged when their dispersion is high.
Stale-snapshot quarantine: an observation whose canonical reset time moves backward relative to newer observations of the same window (replayed or cached provider state, documented in openai/codex issue 23190) is classified `stale_quarantined` and excluded rather than allowed to open a spurious epoch.

The fixed-reset-boundary model for both Codex windows is empirically confirmed, not assumed: captured session data shows `reset_at` byte-stable across hundreds of responses while usage climbs, followed by all-at-once rollover, for both the 5-hour and weekly windows (`~/orca/projects/quota-measurement/research/codex-5h-reset-semantics.md`, high confidence).
Per-epoch cumulative regression is therefore valid for these windows.

### 6.3 Algorithm policy

Within one epoch, fit `p_i = a + b * s_i` by ordinary least squares, where `p_i` is the utilization percent and `s_i` is cumulative attributed spend (tokens primarily, cost secondarily).
The estimands are stated precisely because they differ:

- Marginal capacity `100 / b`: spend corresponding to 100 percentage points of quota, the window's exchange rate.
- Proxied spend at 100 percent `(100 - a) / b`: the fitted proxied spend at which the window exhausts, given whatever non-proxied usage the intercept absorbed.

The intercept does not absorb proxied pre-observation spend; that is already inside `s_i`, which is measured from window start.
It absorbs unproxied usage present at window start plus estimation bias.
The UI's headline number is `TokensAt100`/`CostAt100`, labeled as capacity at the credential's observed workload mix, with the marginal rate in the drill-down.

Token-denominated fits are primary: tokens never reprice, so they survive pricing changes.
Cost-denominated fits are segment-scoped: computed over the longest run of observations sharing one `pricing_snapshot_hash` within the epoch, provided that segment independently passes the gates, and always labeled with the segment's hash and time range (`CostSegment`); when no segment qualifies, cost capacity is suppressed with the `pricing_changed` flag.
Per-observation cumulative repricing makes a mixed cost series internally inconsistent, and pricing changes are the norm for weekly and monthly windows, not an edge case.
Observations with `attributed_cost_complete = false` are likewise excluded from cost fits (`unpriced_models` flag) but retained for token fits.

Uncertainty and stability, sized to this data (rounded, cumulative, autocorrelated, irregularly sampled):

- `EffectiveSamples` counts distinct (percent, spend) change points; heartbeat repeats add rows but not information, and all gates run on effective samples.
- Confidence intervals come from a moving-block bootstrap over the interval-delta series (percent delta against spend delta), not from ordinary OLS standard errors, because cumulative-series OLS errors are spuriously tight under autocorrelation.
- Stability check: the slope fitted on the first half and second half of the epoch (by spend) must agree within 25 percent for `high`, 40 percent for `medium`.
- R-squared is reported as a diagnostic only and participates in no gate: many unrelated monotone cumulative series produce high R-squared.
- Outliers: points with absolute studentized residual above 3 are excluded and classified `outlier`; more than 20 percent outliers caps confidence at `low`.
- Interval regression (treating each percent as a censored interval) remains a later second implementation of the same interface; the bootstrap-plus-stability gates above are the committed v1 uncertainty treatment, not an optional extra.

### 6.4 Confidence and coverage rules

Gates before any estimate is shown (`insufficient` otherwise):

- effective samples >= 4,
- at least 3 distinct percent values,
- percent span >= 10 points and >= 4 times the empirically detected percent resolution of the series,
- positive slope.

Grading when shown:

- `high`: effective samples >= 8, span >= 25 points, bootstrap CI width within 25 percent of the estimate, split-half stability within 25 percent, no flags.
- `medium`: effective samples >= 5, span >= 15 points, CI width within 50 percent, stability within 40 percent, at most minor flags.
- `low`: everything else that passes the gates.

Flags, each with a UI explanation:

- `pricing_changed`: multiple pricing hashes in the epoch; cost capacity is segment-scoped or suppressed (6.3), token capacity retained.
- `unpriced_models`: incomplete cost coverage on some observations; those are excluded from cost fits.
- `coverage_gap`: some interval saw percent rise while attributed spend did not rise at all.
  This is the zero-coverage bypass signal; it is deliberately model-free to avoid a contaminated fit refereeing its own contamination.
  Contaminated intervals are classified `coverage_gap_interval` and excluded from the fit; any detected coverage gap caps confidence at `low` (below the UI cutover), and above 30 percent contaminated intervals the estimate is suppressed to `insufficient` with the flag carried.
  Bypass traffic concurrent with proxied traffic is not detectable by this rule; the fitted slope then conflates proxied and concurrent-bypass consumption, and estimates are biased accordingly.
  This is a documented v1 limitation, stated in the drill-down UI copy; the golden datasets include concurrent-bypass scenarios whose expected outcome is a biased estimate with no flag, asserting the limitation rather than hiding it.
- `mix_shift`: the attributed token-bucket composition shifted materially within the epoch (for example the cache-read share or output share changed by more than a threshold); caps confidence at `medium`, because the token estimand is mix-specific (section 3).
- `reset_ambiguous`: only derived reset times with high dispersion.
- `identity_changed`: an incarnation break occurred nearby; the affected series is suppressed until the next natural reset (6.2).
- `identity_unverified`: the provider exposes no incarnation discriminator (currently Claude), so incarnation stability cannot be checked.
- `stale`: newest observation older than 10 percent of the window length.

Residual-based refinement of coverage detection against clean-interval fits is slice 5, explicitly after the estimator has real data to validate against; until then the `low` cap on coverage-gapped epochs keeps contaminated estimates off the headline.
All thresholds ship as package constants and are tuned against slice-1 data (decision 5).

## 7. API and UI changes

### 7.1 API

Three admin-only endpoints in `internal/api/quota.go`, response shapes exposed as quota-package DTOs through the `QuotaProvider` seam, matching the existing pattern:

- `GET /api/v1/quota/observations` (slice 1) with required `auth_index`, `window_kind_id`, and a required time range capped at 90 days, ordered by `observed_at` ascending, hard row cap 5000 with a truncation marker.
  This is also slice 1's verification surface.
- `POST /api/v1/quota/capacity` (slice 2) with `{"auth_indexes": [...]}` mirroring `POST /quota/cache`, so the paged Credentials UI batches one request per page.
  Returns, per credential and estimable window kind: the current-epoch estimate (without `Points`, for payload size) and up to the last 8 completed-epoch estimates for the comparison strip.
  Empty history returns an empty list for the credential; a present-but-unestimable series returns an `insufficient` estimate with its flags, so the UI distinguishes "no data" from "not enough data".
- `GET /api/v1/quota/capacity/detail` (slice 2, consumed by slice 4) with required `auth_index`, `window_kind_id`, optional epoch selector: one full `WindowEstimate` including `Points`, plus the observation series it was fitted on, in one response, so the drill-down chart and its diagnostics are always consistent with each other and the row cap cannot desynchronize them.

Malformed parameters return 400 in the standard `{"error": ...}` shape.
All three read through the reader pool and call the estimator; nothing derived is stored.

### 7.2 UI

The Credentials page stays lean; depth goes into a drill-down.

1. Quota bar augmentation.
   In estimated mode, the view model prefers the regression estimate when confidence is `medium` or `high`; for `low` and `insufficient` it keeps the existing one-point extrapolation and shows a small history hint on `low`.
   A confidence badge renders subtle for `high`, amber for `medium`.
   When token capacity is available but cost capacity is suppressed or segment-scoped, the display shows tokens with a flag tooltip; `QuotaWindowUsageDisplay` becomes partial-capable (optional cost string) as part of this slice.
   Capacity copy says "at your recent usage mix"; it never claims provider capacity in the abstract.
   Fresh installs regress nothing while observations accrue.
2. Capacity drill-down modal per credential, backed by the detail endpoint.
   Scatter of attributed spend versus percent with the fitted line and the `At100` intercept marked, point classifications rendered from `Points` (outliers, coverage-gap intervals, quarantined and pre-break points visually distinct), an epoch selector, flag explanations including the concurrent-bypass limitation, and a capacity-over-epochs comparison strip keyed by `window_kind_id` so role changes cannot split the series.
3. i18n keys for all three languages in `web/src/i18n/index.ts`.

New fetchers in `web/src/lib/api.ts`, types in `web/src/lib/types.ts`, view-model logic in `credentialViewModels.ts`.

## 8. Operational and privacy considerations

- No new secrets; observations carry identity references, percentages, token sums, and cost.
- Prompts, responses, raw auth JSON, and provider tokens remain unpersisted.
- Write volume: about 288 rows per window kind per day in normal operation, with an absolute recorder-enforced cap of 400; realistic active-day volume is tens of rows.
  Storage at the cap is a few MB per credential-year; retention thin-out is deferred but designed for (delete-only).
- The recorder is asynchronous behind a bounded queue; producers never block on it, and the writer transaction is short (one indexed read, small insert).
  A slice-1 load test exercises concurrent ingestion, refresh, and header traffic.
- Provider quota endpoints remain account-internal and unstable; this design adds no new provider calls and inherits the existing handlers' isolation and error caching.

## 9. Testing strategy

- Estimator unit tests (pure): golden synthetic datasets with known capacity; rounded and sub-integer percents; zero-coverage bypass intervals (expected: flagged, excluded, confidence capped at low); concurrent bypass (expected: biased estimate, no flag, asserting the documented limitation); mid-epoch pricing changes with and without a qualifying segment; unpriced-model gaps; mix-shift scenarios; epoch jitter at and beyond both tolerances; utilization drops, plan and account changes with post-break suppression; stale-snapshot quarantine; degenerate series (flat, two-point, non-monotonic, heartbeat-only); bootstrap CI and split-half stability behavior on stable versus unstable synthetic slopes.
- Epoch assignment table tests: absolute, derived, and mixed reset metadata; anchor determinism under permuted arrival order.
- Recorder tests, through the quota-service level with faked provider transport: recording-policy sequences assert exactly which rows persist, including cheap-gate ordering (no attribution query for skipped candidates), heartbeat, spacing, the 400-per-day safety cap, reset-boundary exception, cold-restart behavior, concurrent-writer duplicate prevention, queue overflow dropping with logged counts, pre-rejection header recording, and non-blocking insert failure.
- Attribution tests, through the recorder's attribution interface (an internal seam of the quota package, public to its own tests): window arithmetic against `quotaRowUsageWindow` conventions, auth-type filtering, bucket composition sums, `attributed_cost_complete` on unpriced models, triggering-event inclusion on the header path and absence on the poll path, and pricing-hash consistency between the cost and the recorded hash.
  The existing display-oriented window-stats path keeps its own tests unchanged.
- Pricing hash tests: stable across recompiles of identical settings; sensitive to every cost-affecting field including pricing style; insensitive to ids, timestamps, and rule order.
- Repository and migration tests: in-memory SQLite per `internal/repository/test` patterns; both registry tests updated; fresh and upgraded schema verification; idempotent migration.
- Load test (slice 1): concurrent usage ingestion, poll refreshes, and header traffic against the single-writer database, asserting no producer stalls.
- API handler tests: all three endpoints; batch shapes, empty versus insufficient, range caps and truncation, detail-endpoint point classifications, malformed input.
- Frontend: view-model tests for estimate-versus-fallback selection per confidence grade, partial and segment-scoped cost display, badge mapping, and point-classification rendering, mirroring the existing view-model test style.

## 10. Staged implementation plan

Each slice is independently useful and shippable.

1. Observation persistence.
   Entity, migration plus registry-test updates, repository, pricing snapshot hash, `QuotaReading` context, async recorder with attribution and recording policy, both producer hook-ins, the observations listing endpoint, and the load test.
   History starts accruing immediately, which is the scarce resource, and the endpoint validates recording from day one.
2. Estimation module plus capacity APIs.
   `internal/quota/estimate` with OLS, epochs, bootstrap uncertainty, stability checks, outliers, confidence, flags, point diagnostics; the capacity and capacity-detail endpoints.
3. Credentials UI integration.
   Regression estimate with confidence badge, partial and segment-scoped cost handling in the quota panel; one-point fallback preserved.
4. Drill-down modal.
   Scatter with fit and point classifications, epoch comparison strip, flag explanations, detail-endpoint wiring.
5. Coverage hardening.
   Residual-based refinement of coverage detection against clean-interval fits, threshold tuning against accrued data, retention thin-out if volume warrants.
6. Optional: interval-regression estimator as a second `Estimator` implementation.

## 11. Recorded decisions (grilling session, 2026-07-23)

All previously open questions are resolved; these are binding for the spec.

1. Documentation home: `docs/design/` in this repo, establishing the convention.
2. Attributed cost is frozen at capture time, no repricing and no rebuild tool; a price correction starts a new pricing-hash segment rather than rewriting history.
3. Capacity is token-denominated first; cost capacity is shown only when a pricing-pure segment qualifies, suppressed with `pricing_changed` otherwise (segment scoping per 6.3, refined by review resolution 17).
4. Observation recording is always on, with no setting and no kill switch.
5. Thresholds, heartbeat, spacing, and epoch tolerances ship as package constants at the values in this document and are tuned against slice-1 data (slice 5).
6. UI cutover: regression estimates replace the one-point extrapolation at `medium` and `high` confidence; `low` keeps the one-point value with a history hint; `insufficient` keeps it silently.
7. Estimates and observation history are admin-only; the key-viewer role never sees them.
8. Feature-scoped quotas are stored but permanently excluded from estimation; no subset-attribution work is planned.
9. Attribution boundary: half-open `[start, observed_at)`; superseded on the header path by review resolution 5, which additionally includes the triggering event by key (post-request header semantics); the poll path keeps the pure half-open rule.
10. Retention: none initially; any future thin-out is delete-only.
11. Codex reset semantics: fixed, account-anchored epochs for both main windows, empirically confirmed (see 6.2); the epoch model applies the provenance-dependent tolerances and stale-snapshot quarantine from the research.

## 12. Adversarial spec review resolutions (gpt-5.6-sol, 2026-07-23)

An adversarial review of the published spec (18 findings, 5 blockers) drove the following resolutions, all folded into the sections above:

1. The design document and tracker configuration are committed to the repository so the spec's authoritative references exist at the implementation baseline (blocker 1).
2. Observations preserve raw provider values: `remaining` and `remaining_fraction` columns added, raw reset representation stored verbatim, and the write-time `percent_resolution` column removed in favor of read-time empirical detection (blocker 2; invariant 3).
3. Credential incarnation is an explicit domain concept: account and plan snapshots on every observation, forced series breaks on change, estimation suppressed until the next natural reset after a break, and an explicit `identity_unverified` state for providers with no discriminator (blocker 3).
4. The token estimand is defined as workload-mix-specific proxied capacity, never abstract provider capacity; bucket composition is persisted per observation and a `mix_shift` flag caps confidence (blocker 4).
5. The header-path attribution boundary includes the triggering event by key, per conventional post-request rate-limit header semantics; this supersedes grilling decision 9 for the header path only (blocker 5).
6. Recording is specified as a sampled contract; the upstream header coalescing points are mapped in 2.1, header observations are recorded before cache-staleness rejection, and stale readings are excluded at read time (finding 6).
7. Window identity is the canonical role-independent `window_kind_id`; raw key, group, and role are provenance only; recording, indexing, estimation, and API selection all use the canonical id (finding 7).
8. Producers hand the recorder an immutable `QuotaReading` context with `observed_at` captured exactly once (finding 8).
9. Bypass detection is scoped honestly to zero-coverage gaps; coverage-gapped epochs cap at `low`, below the UI cutover; concurrent bypass is a documented, golden-tested limitation until slice 5 (finding 9).
10. Confidence gating drops R-squared, adds moving-block bootstrap intervals on the delta series and split-half stability checks; R-squared is diagnostic only (finding 10).
11. The recorder is asynchronous behind a bounded queue with cheap-gate-first ordering, an indexed new-events check before any attribution query, and a slice-1 load test (finding 11).
12. A capacity-detail endpoint returns the full estimate with per-observation `PointDiagnostic` classifications plus its exact observation series, so the drill-down never reimplements or desynchronizes from the estimator (finding 12).
13. Attribution has its own interface (an internal seam of the quota package) whose contract covers credential, boundary, triggering event, composition, completeness, and snapshot identity; the existing display-oriented window-stats seam is untouched (finding 13).
14. The pricing hash payload is defined field by field and includes pricing style; hash tests cover style sensitivity and rule-order insensitivity (finding 14).
15. 288 rows per window kind per day is a normal-operation bound; a recorder-enforced absolute cap of 400 with logged refusal is the hard limit (finding 15).
16. Slice 1's verification surface is the observations listing endpoint, moved from slice 2, with a defined contract (finding 16).
17. Mixed-pricing cost behavior is segment-scoped: the longest qualifying single-hash segment is fitted and labeled with its hash and range; suppression applies only when no segment qualifies (finding 17).
18. Attribution scope is explicit: computed only for estimable windows; null means not computed, zero means no traffic (finding 18).
