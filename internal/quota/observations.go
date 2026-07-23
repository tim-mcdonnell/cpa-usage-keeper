package quota

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/timeutil"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	quotaObservationQueueSize      = 100
	quotaObservationMinimumSpacing = 5 * time.Minute
	quotaObservationHeartbeat      = 30 * time.Minute
	quotaObservationDailyLimit     = 400
	quotaObservationSeriesLimit    = 5000
	quotaObservationMaximumRange   = 90 * 24 * time.Hour

	quotaWindowProviderUnknown = "unknown_provider"
	quotaWindowFeatureOverall  = "overall"
	quotaWindowLimitNone       = "none"
)

type ObservationSeriesRequest struct {
	AuthIndex    string
	WindowKindID string
	Start        time.Time
	End          time.Time
}

type ObservationSeriesResponse struct {
	Items     []entities.QuotaObservation `json:"items"`
	Truncated bool                        `json:"truncated"`
}

// QuotaReading 是 producer 在 cache mutation 前交给 recorder 的不可变值快照。
type QuotaReading struct {
	identity   entities.UsageIdentity
	provider   string
	source     RefreshSource
	observedAt time.Time
	rows       []QuotaRow
}

type quotaObservationStore interface {
	MaxUsageEventID(context.Context, string, string) (int64, error)
	SumAttributedUsage(context.Context, string, string, time.Time, time.Time, pricing.Resolver) (repository.QuotaAttributedUsage, error)
	InsertIfDue(context.Context, entities.QuotaObservation, time.Duration, int64) (repository.QuotaObservationInsertResult, error)
}

type repositoryQuotaObservationStore struct {
	db *gorm.DB
}

func (s repositoryQuotaObservationStore) MaxUsageEventID(ctx context.Context, authType string, authIndex string) (int64, error) {
	return repository.MaxUsageEventIDForCredential(ctx, s.db, authType, authIndex)
}

func (s repositoryQuotaObservationStore) SumAttributedUsage(
	ctx context.Context,
	authType string,
	authIndex string,
	start time.Time,
	end time.Time,
	resolver pricing.Resolver,
) (repository.QuotaAttributedUsage, error) {
	return repository.SumQuotaAttributedUsage(ctx, s.db, authType, authIndex, start, end, resolver)
}

func (s repositoryQuotaObservationStore) InsertIfDue(
	ctx context.Context,
	observation entities.QuotaObservation,
	minimumSpacing time.Duration,
	dailyLimit int64,
) (repository.QuotaObservationInsertResult, error) {
	return repository.InsertQuotaObservationIfDue(ctx, s.db, observation, minimumSpacing, dailyLimit)
}

type quotaObservationSeriesKey struct {
	usageIdentityID int64
	windowKindID    string
}

type quotaObservationRecordedState struct {
	observedAt            time.Time
	usedPercent           *float64
	resetAt               *time.Time
	resetRaw              *string
	resetAfterSeconds     *int64
	usageEventWatermarkID int64
}

type quotaObservationRecorder struct {
	store   quotaObservationStore
	pricing *pricing.Catalog

	queueMu   sync.Mutex
	queue     chan QuotaReading
	stopCh    chan struct{}
	doneCh    chan struct{}
	closing   bool
	closeOnce sync.Once
	dropped   atomic.Uint64
}

func newQuotaObservationRecorder(store quotaObservationStore, pricingCatalog *pricing.Catalog, queueSize int) *quotaObservationRecorder {
	if queueSize <= 0 {
		queueSize = quotaObservationQueueSize
	}
	recorder := &quotaObservationRecorder{
		store:   store,
		pricing: pricingCatalog,
		queue:   make(chan QuotaReading, queueSize),
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	go recorder.run()
	return recorder
}

func (r *quotaObservationRecorder) enqueue(reading QuotaReading) {
	if r == nil {
		return
	}
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	if r.closing {
		return
	}
	select {
	case r.queue <- reading:
		return
	default:
	}
	select {
	case <-r.queue:
	default:
	}
	r.queue <- reading
	logrus.WithField("dropped_count", r.dropped.Add(1)).Warn("quota observation queue full; dropped oldest reading")
}

func (r *quotaObservationRecorder) stop() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		r.queueMu.Lock()
		r.closing = true
		close(r.stopCh)
		r.queueMu.Unlock()
		<-r.doneCh
	})
}

func (r *quotaObservationRecorder) run() {
	defer close(r.doneCh)
	states := make(map[quotaObservationSeriesKey]quotaObservationRecordedState)
	for {
		select {
		case reading := <-r.queue:
			r.record(context.Background(), reading, states)
		case <-r.stopCh:
			for {
				select {
				case reading := <-r.queue:
					r.record(context.Background(), reading, states)
				default:
					return
				}
			}
		}
	}
}

func (r *quotaObservationRecorder) record(
	ctx context.Context,
	reading QuotaReading,
	states map[quotaObservationSeriesKey]quotaObservationRecordedState,
) {
	if r == nil || r.store == nil || r.pricing == nil {
		return
	}
	authType := quotaObservationAuthType(reading.identity)
	for _, row := range reading.rows {
		observation := newQuotaObservation(reading, authType, row)
		key := quotaObservationSeriesKey{
			usageIdentityID: observation.UsageIdentityID,
			windowKindID:    observation.WindowKindID,
		}
		previous, found := states[key]
		resetChanged := found && quotaRecordedResetChanged(previous, observation)
		accepted := !found || resetChanged || !equalFloat64Pointers(previous.usedPercent, observation.UsedPercent)
		watermark := previous.usageEventWatermarkID
		watermarkLoaded := false
		if !accepted {
			var err error
			watermark, err = r.store.MaxUsageEventID(ctx, observation.AuthType, observation.AuthIndex)
			if err != nil {
				r.logFailure(observation, "load usage event watermark", err)
				continue
			}
			watermarkLoaded = true
			accepted = watermark > previous.usageEventWatermarkID ||
				reading.observedAt.Sub(previous.observedAt) >= quotaObservationHeartbeat
		}
		if !accepted {
			continue
		}
		if found && !resetChanged && reading.observedAt.Sub(previous.observedAt) < quotaObservationMinimumSpacing {
			continue
		}

		resolver := r.pricing.NewResolver()
		if quotaObservationIsEstimable(observation) {
			if start, ok := quotaObservationAttributionStart(observation); ok {
				attribution, err := r.store.SumAttributedUsage(
					ctx,
					observation.AuthType,
					observation.AuthIndex,
					start,
					observation.ObservedAt,
					resolver,
				)
				if err != nil {
					r.logFailure(observation, "compute attributed usage", err)
					continue
				}
				applyQuotaAttribution(&observation, attribution)
			}
		}
		if !watermarkLoaded {
			var err error
			watermark, err = r.store.MaxUsageEventID(ctx, observation.AuthType, observation.AuthIndex)
			if err != nil {
				r.logFailure(observation, "load usage event watermark", err)
				continue
			}
		}
		observation.UsageEventWatermarkID = watermark
		if observation.PricingSnapshotHash == "" {
			observation.PricingSnapshotHash = resolver.ContentHash()
		}

		result, err := r.store.InsertIfDue(ctx, observation, quotaObservationMinimumSpacing, quotaObservationDailyLimit)
		if err != nil {
			r.logFailure(observation, "insert observation", err)
			continue
		}
		switch result {
		case repository.QuotaObservationInserted, repository.QuotaObservationSkipped:
			states[key] = quotaObservationStateFromEntity(observation)
		case repository.QuotaObservationDailyLimit:
			logrus.WithFields(logrus.Fields{
				"auth_index":     observation.AuthIndex,
				"window_kind_id": observation.WindowKindID,
				"daily_limit":    quotaObservationDailyLimit,
			}).Warn("quota observation daily limit reached; reading refused")
		}
	}
}

func (r *quotaObservationRecorder) logFailure(observation entities.QuotaObservation, operation string, err error) {
	logrus.WithError(err).WithFields(logrus.Fields{
		"auth_index":     observation.AuthIndex,
		"window_kind_id": observation.WindowKindID,
		"operation":      operation,
	}).Error("quota observation recording failed")
}

func newQuotaReading(
	identity entities.UsageIdentity,
	provider string,
	source RefreshSource,
	observedAt time.Time,
	rows []QuotaRow,
) QuotaReading {
	return QuotaReading{
		identity:   cloneQuotaObservationIdentity(identity),
		provider:   strings.ToLower(strings.TrimSpace(provider)),
		source:     source,
		observedAt: timeutil.NormalizeStorageTime(observedAt),
		rows:       cloneQuotaRows(rows),
	}
}

func newQuotaObservation(reading QuotaReading, authType string, row QuotaRow) entities.QuotaObservation {
	windowKindID := quotaWindowKindID(reading.provider, row)
	resetAt := normalizedQuotaObservationReset(row, reading.observedAt)
	resetRaw := nullableVerbatimString(row.ResetRaw)
	if resetRaw == nil {
		resetRaw = nullableVerbatimString(row.ResetAt)
	}
	planType := nullableTrimmedString(row.PlanType)
	if planType == nil {
		planType = cloneStringPointer(reading.identity.PlanType)
	}
	observation := entities.QuotaObservation{
		UsageIdentityID:      reading.identity.ID,
		AuthType:             authType,
		AuthIndex:            strings.TrimSpace(reading.identity.Identity),
		AccountID:            cloneStringPointer(reading.identity.AccountID),
		PlanType:             planType,
		Provider:             reading.provider,
		WindowKindID:         windowKindID,
		QuotaKey:             row.Key,
		Scope:                row.Scope,
		GroupKey:             row.GroupKey,
		WindowRole:           row.WindowRole,
		WindowSeconds:        quotaRowWindowSeconds(row),
		ObservedAt:           reading.observedAt,
		Source:               string(reading.source),
		UsedPercent:          cloneFloat64Pointer(row.UsedPercent),
		PercentSource:        row.PercentSource,
		RemainingFraction:    cloneFloat64Pointer(row.RemainingFraction),
		Used:                 cloneFloat64Pointer(row.Used),
		LimitValue:           cloneFloat64Pointer(row.Limit),
		Remaining:            cloneFloat64Pointer(row.Remaining),
		ResetAt:              resetAt,
		ResetRaw:             resetRaw,
		ResetAfterSeconds:    cloneInt64Pointer(row.ResetAfterSeconds),
		ProviderWindowTokens: cloneInt64Pointer(row.WindowUsageTokens),
		ProviderWindowCost:   cloneFloat64Pointer(row.WindowUsageCost),
		PricingSnapshotHash:  "",
		CreatedAt:            reading.observedAt,
	}
	return observation
}

func applyQuotaAttribution(observation *entities.QuotaObservation, attribution repository.QuotaAttributedUsage) {
	if observation == nil {
		return
	}
	observation.AttributedTokens = int64Pointer(attribution.TotalTokens)
	observation.AttributedInputTokens = int64Pointer(attribution.InputTokens)
	observation.AttributedOutputTokens = int64Pointer(attribution.OutputTokens)
	observation.AttributedCacheReadTokens = int64Pointer(attribution.CacheReadTokens)
	observation.AttributedCacheCreationTokens = int64Pointer(attribution.CacheCreationTokens)
	observation.AttributedCostUSD = float64Pointer(attribution.CostUSD)
	observation.AttributedCostComplete = attribution.CostComplete
	observation.PricingSnapshotHash = attribution.PricingSnapshotHash
}

// quotaWindowKindID implements the stable v1 storage/API contract:
// provider/metered_feature/limit_id/window_seconds.
func quotaWindowKindID(provider string, row QuotaRow) string {
	provider = quotaWindowKindComponent(provider, quotaWindowProviderUnknown)
	feature := quotaWindowMeteredFeature(provider, row)
	limitID := quotaWindowStableLimitID(row)
	seconds := int64(0)
	if row.Window != nil && row.Window.Seconds != nil && *row.Window.Seconds > 0 {
		seconds = *row.Window.Seconds
	}
	return strings.Join([]string{provider, feature, limitID, strconv.FormatInt(seconds, 10)}, "/")
}

func quotaWindowMeteredFeature(provider string, row QuotaRow) string {
	key := strings.ToLower(strings.TrimSpace(row.Key))
	switch provider {
	case "codex":
		switch {
		case strings.HasPrefix(key, "rate_limit."):
			return quotaWindowFeatureOverall
		case strings.HasPrefix(key, "code_review_rate_limit."):
			return "code_review"
		case strings.HasPrefix(key, "additional_rate_limits."):
			return quotaWindowKindComponent(firstNonEmpty(row.Metric, row.Scope), quotaWindowFeatureOverall)
		}
	case "claude":
		switch key {
		case "five_hour", "seven_day":
			return quotaWindowFeatureOverall
		case "seven_day_cowork":
			return "cowork"
		case "seven_day_oauth_apps":
			return "oauth_apps"
		case "seven_day_opus":
			return "opus"
		case "seven_day_sonnet":
			return "sonnet"
		}
	}
	if strings.EqualFold(strings.TrimSpace(row.Scope), "window") {
		return quotaWindowFeatureOverall
	}
	return quotaWindowKindComponent(firstNonEmpty(row.Metric, row.Scope, quotaWindowFeatureOverall), quotaWindowFeatureOverall)
}

func quotaWindowStableLimitID(row QuotaRow) string {
	key := strings.TrimSpace(row.Key)
	key = strings.TrimSuffix(key, ".primary_window")
	key = strings.TrimSuffix(key, ".secondary_window")
	if key == "" {
		return quotaWindowLimitNone
	}
	return quotaWindowKindComponent(key, quotaWindowLimitNone)
}

func quotaWindowKindComponent(value string, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	pendingSeparator := false
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			if pendingSeparator && result.Len() > 0 {
				result.WriteByte('_')
			}
			result.WriteRune(character)
			pendingSeparator = false
			continue
		}
		if result.Len() > 0 {
			pendingSeparator = true
		}
	}
	if result.Len() == 0 {
		return fallback
	}
	return result.String()
}

func quotaObservationIsEstimable(observation entities.QuotaObservation) bool {
	if observation.WindowSeconds == nil {
		return false
	}
	switch observation.WindowKindID {
	case "claude/overall/five_hour/18000",
		"claude/overall/seven_day/604800",
		"codex/overall/rate_limit/18000",
		"codex/overall/rate_limit/604800":
		return true
	default:
		return false
	}
}

func quotaObservationAttributionStart(observation entities.QuotaObservation) (time.Time, bool) {
	if observation.ResetAt == nil || observation.WindowSeconds == nil || *observation.WindowSeconds <= 0 {
		return time.Time{}, false
	}
	return observation.ResetAt.Add(-time.Duration(*observation.WindowSeconds) * time.Second), true
}

func normalizedQuotaObservationReset(row QuotaRow, observedAt time.Time) *time.Time {
	if strings.TrimSpace(row.ResetAt) != "" {
		parsed, err := timeutil.ParseStorageTime(row.ResetAt)
		if err == nil {
			normalized := timeutil.NormalizeStorageTime(parsed)
			return &normalized
		}
	}
	if row.ResetAfterSeconds != nil && *row.ResetAfterSeconds >= 0 {
		normalized := timeutil.NormalizeStorageTime(observedAt).Add(time.Duration(*row.ResetAfterSeconds) * time.Second)
		return &normalized
	}
	return nil
}

func quotaObservationAuthType(identity entities.UsageIdentity) string {
	if value := strings.ToLower(strings.TrimSpace(identity.AuthTypeName)); value == "oauth" || value == "apikey" {
		return value
	}
	switch identity.AuthType {
	case entities.UsageIdentityAuthTypeAuthFile:
		return "oauth"
	case entities.UsageIdentityAuthTypeAIProvider:
		return "apikey"
	default:
		return ""
	}
}

func quotaRecordedResetChanged(previous quotaObservationRecordedState, candidate entities.QuotaObservation) bool {
	if previous.resetAt != nil || candidate.ResetAt != nil {
		if previous.resetAt == nil || candidate.ResetAt == nil {
			return true
		}
		return !previous.resetAt.Equal(*candidate.ResetAt)
	}
	if !equalStringPointers(previous.resetRaw, candidate.ResetRaw) {
		return true
	}
	return !equalInt64Pointers(previous.resetAfterSeconds, candidate.ResetAfterSeconds)
}

func quotaObservationStateFromEntity(observation entities.QuotaObservation) quotaObservationRecordedState {
	return quotaObservationRecordedState{
		observedAt:            observation.ObservedAt,
		usedPercent:           cloneFloat64Pointer(observation.UsedPercent),
		resetAt:               cloneTimePointer(observation.ResetAt),
		resetRaw:              cloneStringPointer(observation.ResetRaw),
		resetAfterSeconds:     cloneInt64Pointer(observation.ResetAfterSeconds),
		usageEventWatermarkID: observation.UsageEventWatermarkID,
	}
}

func (s *Service) ListObservations(ctx context.Context, request ObservationSeriesRequest) (ObservationSeriesResponse, error) {
	authIndex := strings.TrimSpace(request.AuthIndex)
	windowKindID := strings.TrimSpace(request.WindowKindID)
	if authIndex == "" || windowKindID == "" || request.Start.IsZero() || request.End.IsZero() {
		return ObservationSeriesResponse{}, fmt.Errorf("%w: auth_index, window_kind_id, start, and end are required", ErrValidation)
	}
	if !request.Start.Before(request.End) || request.End.Sub(request.Start) > quotaObservationMaximumRange {
		return ObservationSeriesResponse{}, fmt.Errorf("%w: observation time range is invalid", ErrValidation)
	}
	identity, err := repository.GetActiveAuthFileUsageIdentityByAuthIndex(ctx, s.db, authIndex)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ObservationSeriesResponse{}, fmt.Errorf("%w: %s", ErrNotFound, authIndex)
		}
		return ObservationSeriesResponse{}, err
	}
	items, truncated, err := repository.ListQuotaObservations(
		ctx,
		s.db,
		identity.ID,
		windowKindID,
		request.Start,
		request.End,
		quotaObservationSeriesLimit,
	)
	if err != nil {
		return ObservationSeriesResponse{}, err
	}
	return ObservationSeriesResponse{Items: items, Truncated: truncated}, nil
}

func cloneQuotaRows(rows []QuotaRow) []QuotaRow {
	cloned := make([]QuotaRow, len(rows))
	for index := range rows {
		cloned[index] = rows[index]
		cloned[index].Used = cloneFloat64Pointer(rows[index].Used)
		cloned[index].Limit = cloneFloat64Pointer(rows[index].Limit)
		cloned[index].Remaining = cloneFloat64Pointer(rows[index].Remaining)
		cloned[index].UsedPercent = cloneFloat64Pointer(rows[index].UsedPercent)
		cloned[index].RemainingFraction = cloneFloat64Pointer(rows[index].RemainingFraction)
		cloned[index].ResetAfterSeconds = cloneInt64Pointer(rows[index].ResetAfterSeconds)
		cloned[index].WindowUsageTokens = cloneInt64Pointer(rows[index].WindowUsageTokens)
		cloned[index].WindowUsageCost = cloneFloat64Pointer(rows[index].WindowUsageCost)
		if rows[index].Window != nil {
			window := *rows[index].Window
			window.Duration = cloneFloat64Pointer(rows[index].Window.Duration)
			window.Seconds = cloneInt64Pointer(rows[index].Window.Seconds)
			cloned[index].Window = &window
		}
	}
	return cloned
}

func cloneQuotaObservationIdentity(identity entities.UsageIdentity) entities.UsageIdentity {
	cloned := identity
	cloned.AccountID = cloneStringPointer(identity.AccountID)
	cloned.PlanType = cloneStringPointer(identity.PlanType)
	return cloned
}

func quotaRowWindowSeconds(row QuotaRow) *int64 {
	if row.Window == nil {
		return nil
	}
	return cloneInt64Pointer(row.Window.Seconds)
}

func nullableTrimmedString(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func nullableVerbatimString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	cloned := value
	return &cloned
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneFloat64Pointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func equalFloat64Pointers(left *float64, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func equalStringPointers(left *string, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func equalInt64Pointers(left *int64, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func int64Pointer(value int64) *int64 {
	return &value
}

func float64Pointer(value float64) *float64 {
	return &value
}
