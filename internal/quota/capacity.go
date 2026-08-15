package quota

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/quota/estimate"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/timeutil"

	"gorm.io/gorm"
)

const (
	capacityCompletedEpochLimit = 8
	capacityEpochReadCount      = capacityCompletedEpochLimit + 1
	capacityReadBoundaryDays    = 1
	CapacityMaxAuthIndexes      = 100
)

var capacityWindowKinds = []string{
	estimate.WindowKindClaudeFiveHour,
	estimate.WindowKindClaudeSevenDay,
	estimate.WindowKindCodexFiveHour,
	estimate.WindowKindCodexSevenDay,
}

type CapacityRequest struct {
	AuthIndexes []string `json:"auth_indexes"`
}

type CapacityResponse struct {
	Items []CredentialCapacity `json:"items"`
}

type CredentialCapacity struct {
	AuthIndex string           `json:"auth_index"`
	Windows   []CapacityWindow `json:"windows"`
}

type CapacityWindow struct {
	Provider      string                    `json:"provider"`
	WindowKindID  string                    `json:"window_kind_id"`
	WindowSeconds int64                     `json:"window_seconds"`
	CurrentEpoch  *estimate.WindowEstimate  `json:"current_epoch"`
	RecentEpochs  []estimate.WindowEstimate `json:"recent_epochs"`
}

type CapacityDetailRequest struct {
	AuthIndex    string
	WindowKindID string
	EpochResetAt *time.Time
}

type CapacityDetailResponse struct {
	Estimate     estimate.WindowEstimate     `json:"estimate"`
	Observations []entities.QuotaObservation `json:"observations"`
	Epochs       []estimate.WindowEstimate   `json:"epochs"`
}

func (s *Service) GetCapacity(ctx context.Context, request CapacityRequest) (CapacityResponse, error) {
	authIndexes := normalizeCapacityAuthIndexes(request.AuthIndexes)
	if err := validateNormalizedCapacityAuthIndexes(authIndexes); err != nil {
		return CapacityResponse{}, err
	}
	identities, err := repository.ListActiveAuthFileUsageIdentitiesByAuthIndexes(ctx, s.db, authIndexes)
	if err != nil {
		return CapacityResponse{}, err
	}
	identityByAuthIndex := make(map[string]entities.UsageIdentity, len(identities))
	for _, identity := range identities {
		identityByAuthIndex[strings.TrimSpace(identity.Identity)] = identity
	}
	now := timeutil.NormalizeStorageTime(time.Now())
	response := CapacityResponse{Items: make([]CredentialCapacity, 0, len(identities))}
	for _, authIndex := range authIndexes {
		identity, ok := identityByAuthIndex[authIndex]
		if !ok {
			continue
		}
		item := CredentialCapacity{
			AuthIndex: authIndex,
			Windows:   make([]CapacityWindow, 0),
		}
		for _, windowKindID := range capacityWindowKinds {
			observations, err := s.recentCapacityObservations(ctx, identity.ID, windowKindID)
			if err != nil {
				return CapacityResponse{}, err
			}
			if len(observations) == 0 {
				continue
			}
			estimates := s.estimator.EstimateWindows(observations, now)
			window, ok := capacityWindowFromEstimates(estimates, now)
			if ok {
				item.Windows = append(item.Windows, window)
			}
		}
		response.Items = append(response.Items, item)
	}
	return response, nil
}

func ValidateCapacityRequest(request CapacityRequest) error {
	return validateNormalizedCapacityAuthIndexes(normalizeCapacityAuthIndexes(request.AuthIndexes))
}

func validateNormalizedCapacityAuthIndexes(authIndexes []string) error {
	if len(authIndexes) == 0 {
		return fmt.Errorf("%w: auth_indexes are required", ErrValidation)
	}
	if len(authIndexes) > CapacityMaxAuthIndexes {
		return ErrCapacityAuthIndexLimit
	}
	return nil
}

func (s *Service) GetCapacityDetail(ctx context.Context, request CapacityDetailRequest) (CapacityDetailResponse, error) {
	authIndex := strings.TrimSpace(request.AuthIndex)
	windowKindID := strings.TrimSpace(request.WindowKindID)
	if authIndex == "" || windowKindID == "" || !estimate.IsEstimableWindowKind(windowKindID) {
		return CapacityDetailResponse{}, fmt.Errorf("%w: auth_index and estimable window_kind_id are required", ErrValidation)
	}
	identity, err := repository.GetActiveAuthFileUsageIdentityByAuthIndex(ctx, s.db, authIndex)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return CapacityDetailResponse{}, fmt.Errorf("%w: %s", ErrNotFound, authIndex)
		}
		return CapacityDetailResponse{}, err
	}
	observations, err := s.recentCapacityObservations(ctx, identity.ID, windowKindID)
	if err != nil {
		return CapacityDetailResponse{}, err
	}
	if len(observations) == 0 {
		return CapacityDetailResponse{}, fmt.Errorf("%w: capacity history", ErrNotFound)
	}
	now := timeutil.NormalizeStorageTime(time.Now())
	estimates := s.estimator.EstimateWindows(observations, now)
	selected, ok := selectCapacityEstimate(estimates, request.EpochResetAt, now)
	if !ok {
		return CapacityDetailResponse{}, fmt.Errorf("%w: capacity epoch", ErrNotFound)
	}
	observationIDs := make(map[int64]struct{}, len(selected.Points))
	for _, point := range selected.Points {
		observationIDs[point.ObservationID] = struct{}{}
	}
	selectedObservations := make([]entities.QuotaObservation, 0, len(observationIDs))
	for _, observation := range observations {
		if _, ok := observationIDs[observation.ID]; ok {
			selectedObservations = append(selectedObservations, observation)
		}
	}
	return CapacityDetailResponse{
		Estimate:     selected,
		Observations: selectedObservations,
		Epochs:       capacityEpochSummaries(estimates, now),
	}, nil
}

func capacityEpochSummaries(estimates []estimate.WindowEstimate, now time.Time) []estimate.WindowEstimate {
	window, ok := capacityWindowFromEstimates(estimates, now)
	if !ok {
		return []estimate.WindowEstimate{}
	}
	result := make([]estimate.WindowEstimate, 0, 1+len(window.RecentEpochs))
	if window.CurrentEpoch != nil {
		result = append(result, *window.CurrentEpoch)
	}
	result = append(result, window.RecentEpochs...)
	return result
}

func (s *Service) recentCapacityObservations(
	ctx context.Context,
	usageIdentityID int64,
	windowKindID string,
) ([]entities.QuotaObservation, error) {
	windowSeconds, ok := capacityWindowSeconds(windowKindID)
	if !ok {
		return []entities.QuotaObservation{}, nil
	}
	return repository.ListRecentQuotaObservations(
		ctx,
		s.db,
		usageIdentityID,
		windowKindID,
		capacityObservationReadLimit(windowSeconds),
	)
}

func capacityWindowFromEstimates(estimates []estimate.WindowEstimate, now time.Time) (CapacityWindow, bool) {
	if len(estimates) == 0 {
		return CapacityWindow{}, false
	}
	window := CapacityWindow{
		Provider:      estimates[0].Provider,
		WindowKindID:  estimates[0].WindowKindID,
		WindowSeconds: estimates[0].WindowSeconds,
		RecentEpochs:  make([]estimate.WindowEstimate, 0, capacityCompletedEpochLimit),
	}
	for _, value := range estimates {
		summary := estimate.WithoutDetail(value)
		if value.EpochResetAt == nil || value.EpochResetAt.After(now) {
			if window.CurrentEpoch == nil {
				window.CurrentEpoch = &summary
			}
			continue
		}
		if len(window.RecentEpochs) < capacityCompletedEpochLimit {
			window.RecentEpochs = append(window.RecentEpochs, summary)
		}
	}
	return window, true
}

func selectCapacityEstimate(
	estimates []estimate.WindowEstimate,
	epochResetAt *time.Time,
	now time.Time,
) (estimate.WindowEstimate, bool) {
	if epochResetAt != nil {
		selector := timeutil.NormalizeStorageTime(*epochResetAt)
		for _, value := range estimates {
			if value.EpochResetAt != nil && value.EpochResetAt.Equal(selector) {
				return value, true
			}
		}
		return estimate.WindowEstimate{}, false
	}
	for _, value := range estimates {
		if value.EpochResetAt == nil || value.EpochResetAt.After(now) {
			return value, true
		}
	}
	if len(estimates) > 0 {
		return estimates[0], true
	}
	return estimate.WindowEstimate{}, false
}

func capacityObservationReadLimit(windowSeconds int64) int {
	const daySeconds = int64((24 * time.Hour) / time.Second)
	days := (capacityEpochReadCount*windowSeconds + daySeconds - 1) / daySeconds
	days += capacityReadBoundaryDays
	return int(max(int64(1), days)) * quotaObservationDailyLimit
}

func capacityWindowSeconds(windowKindID string) (int64, bool) {
	switch windowKindID {
	case estimate.WindowKindClaudeFiveHour, estimate.WindowKindCodexFiveHour:
		return int64((5 * time.Hour) / time.Second), true
	case estimate.WindowKindClaudeSevenDay, estimate.WindowKindCodexSevenDay:
		return int64((7 * 24 * time.Hour) / time.Second), true
	default:
		return 0, false
	}
}

func normalizeCapacityAuthIndexes(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
