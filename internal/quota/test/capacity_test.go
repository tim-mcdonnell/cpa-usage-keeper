package test

import (
	"context"
	"errors"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/quota"
	"cpa-usage-keeper/internal/quota/estimate"

	"gorm.io/gorm"
)

type capacityEstimatorStub struct {
	estimates []estimate.WindowEstimate
	calls     int
}

func (s *capacityEstimatorStub) EstimateWindows(
	observations []entities.QuotaObservation,
	now time.Time,
) []estimate.WindowEstimate {
	s.calls++
	result := make([]estimate.WindowEstimate, len(s.estimates))
	copy(result, s.estimates)
	return result
}

func TestCapacityBatchPreservesCredentialOrderCapsHistoryAndDistinguishesEmpty(t *testing.T) {
	db := openQuotaTestDatabase(t)
	dataIdentity := createCapacityIdentity(t, db, "auth-data")
	createCapacityIdentity(t, db, "auth-empty")
	observation := createCapacityObservation(t, db, dataIdentity.ID)
	estimator := &capacityEstimatorStub{
		estimates: capacityStubEstimates(observation.ID),
	}
	service := quota.NewServiceWithRegistryAndOptions(
		db,
		quota.NewProviderRegistry(nil),
		quota.ServiceOptions{
			PricingCatalog: emptyPricingCatalogForTest(),
			Estimator:      estimator,
		},
	)
	t.Cleanup(service.StopRefreshTasks)

	response, err := service.GetCapacity(context.Background(), quota.CapacityRequest{
		AuthIndexes: []string{" auth-empty ", "auth-data", "auth-empty", "missing"},
	})
	if err != nil {
		t.Fatalf("GetCapacity returned error: %v", err)
	}
	if len(response.Items) != 2 ||
		response.Items[0].AuthIndex != "auth-empty" ||
		response.Items[1].AuthIndex != "auth-data" {
		t.Fatalf("unexpected deterministic credential items: %+v", response.Items)
	}
	if len(response.Items[0].Windows) != 0 {
		t.Fatalf("empty history should return empty windows, got %+v", response.Items[0].Windows)
	}
	if len(response.Items[1].Windows) != 1 {
		t.Fatalf("present history should return one window, got %+v", response.Items[1].Windows)
	}
	window := response.Items[1].Windows[0]
	if window.CurrentEpoch == nil || window.CurrentEpoch.Confidence != estimate.ConfidenceInsufficient {
		t.Fatalf("present-but-unestimable current epoch was not retained: %+v", window.CurrentEpoch)
	}
	if len(window.RecentEpochs) != 8 {
		t.Fatalf("recent completed epochs = %d, want 8", len(window.RecentEpochs))
	}
	if len(window.CurrentEpoch.Points) != 0 || len(window.CurrentEpoch.FittedSeries) != 0 {
		t.Fatalf("batch estimate unexpectedly retained detail: %+v", window.CurrentEpoch)
	}
	if estimator.calls != 1 {
		t.Fatalf("estimator calls = %d, want one observed window only", estimator.calls)
	}
}

func TestCapacityDetailReturnsFullEstimateAndExactObservationSeries(t *testing.T) {
	db := openQuotaTestDatabase(t)
	identity := createCapacityIdentity(t, db, "auth-data")
	observation := createCapacityObservation(t, db, identity.ID)
	estimates := capacityStubEstimates(observation.ID)
	estimator := &capacityEstimatorStub{estimates: estimates}
	service := quota.NewServiceWithRegistryAndOptions(
		db,
		quota.NewProviderRegistry(nil),
		quota.ServiceOptions{
			PricingCatalog: emptyPricingCatalogForTest(),
			Estimator:      estimator,
		},
	)
	t.Cleanup(service.StopRefreshTasks)

	response, err := service.GetCapacityDetail(context.Background(), quota.CapacityDetailRequest{
		AuthIndex:    " auth-data ",
		WindowKindID: estimate.WindowKindCodexFiveHour,
	})
	if err != nil {
		t.Fatalf("GetCapacityDetail returned error: %v", err)
	}
	if response.Estimate.EpochResetAt == nil ||
		estimates[0].EpochResetAt == nil ||
		!response.Estimate.EpochResetAt.Equal(*estimates[0].EpochResetAt) ||
		len(response.Estimate.Points) != 1 ||
		len(response.Estimate.FittedSeries) != 1 {
		t.Fatalf("detail estimate lost coherent diagnostics: %+v", response.Estimate)
	}
	if len(response.Observations) != 1 || response.Observations[0].ID != observation.ID {
		t.Fatalf("detail observations do not match estimate points: %+v", response.Observations)
	}
	point := response.Estimate.Points[0]
	fitted := response.Estimate.FittedSeries[0]
	if point.ObservationID != response.Observations[0].ID ||
		fitted.ObservationID != response.Observations[0].ID ||
		point.CumulativePercentOffset != fitted.CumulativePercentOffset {
		t.Fatalf("detail classifications and fitted series disagree: point=%+v fitted=%+v", point, fitted)
	}
}

func TestCapacityStoredHistoryWithoutResetIsInsufficientNotEmpty(t *testing.T) {
	db := openQuotaTestDatabase(t)
	identity := createCapacityIdentity(t, db, "auth-unassigned")
	observation := createCapacityObservation(t, db, identity.ID)
	if err := db.Model(&observation).Update("reset_at", nil).Error; err != nil {
		t.Fatalf("remove reset metadata: %v", err)
	}
	service := quota.NewServiceWithRegistryAndOptions(
		db,
		quota.NewProviderRegistry(nil),
		quota.ServiceOptions{PricingCatalog: emptyPricingCatalogForTest()},
	)
	t.Cleanup(service.StopRefreshTasks)

	response, err := service.GetCapacity(context.Background(), quota.CapacityRequest{
		AuthIndexes: []string{"auth-unassigned"},
	})
	if err != nil {
		t.Fatalf("GetCapacity returned error: %v", err)
	}
	if len(response.Items) != 1 ||
		len(response.Items[0].Windows) != 1 ||
		response.Items[0].Windows[0].CurrentEpoch == nil {
		t.Fatalf("stored unassigned history was reported as empty: %+v", response)
	}
	current := response.Items[0].Windows[0].CurrentEpoch
	if current.Confidence != estimate.ConfidenceInsufficient || current.EpochResetAt != nil {
		t.Fatalf("unassigned current estimate = %+v", current)
	}

	detail, err := service.GetCapacityDetail(context.Background(), quota.CapacityDetailRequest{
		AuthIndex:    "auth-unassigned",
		WindowKindID: estimate.WindowKindCodexFiveHour,
	})
	if err != nil {
		t.Fatalf("GetCapacityDetail returned error: %v", err)
	}
	if detail.Estimate.EpochResetAt != nil ||
		len(detail.Estimate.Points) != 1 ||
		detail.Estimate.Points[0].Class != estimate.PointEpochUnassigned ||
		len(detail.Observations) != 1 ||
		detail.Observations[0].ID != observation.ID {
		t.Fatalf("unassigned detail response = %+v", detail)
	}
}

func TestCapacityDetailEnforcesCredentialWindowAndEpochSelection(t *testing.T) {
	db := openQuotaTestDatabase(t)
	identity := createCapacityIdentity(t, db, "auth-data")
	observation := createCapacityObservation(t, db, identity.ID)
	service := quota.NewServiceWithRegistryAndOptions(
		db,
		quota.NewProviderRegistry(nil),
		quota.ServiceOptions{
			PricingCatalog: emptyPricingCatalogForTest(),
			Estimator:      &capacityEstimatorStub{estimates: capacityStubEstimates(observation.ID)},
		},
	)
	t.Cleanup(service.StopRefreshTasks)

	testCases := []struct {
		name    string
		request quota.CapacityDetailRequest
		want    error
	}{
		{
			name: "feature window",
			request: quota.CapacityDetailRequest{
				AuthIndex:    "auth-data",
				WindowKindID: "codex/feature/code_review/18000",
			},
			want: quota.ErrValidation,
		},
		{
			name: "unknown credential",
			request: quota.CapacityDetailRequest{
				AuthIndex:    "missing",
				WindowKindID: estimate.WindowKindCodexFiveHour,
			},
			want: quota.ErrNotFound,
		},
		{
			name: "unknown epoch",
			request: quota.CapacityDetailRequest{
				AuthIndex:    "auth-data",
				WindowKindID: estimate.WindowKindCodexFiveHour,
				EpochResetAt: timePointer(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)),
			},
			want: quota.ErrNotFound,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := service.GetCapacityDetail(context.Background(), testCase.request)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("GetCapacityDetail error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func createCapacityIdentity(t *testing.T, db *gorm.DB, authIndex string) entities.UsageIdentity {
	t.Helper()
	identity := entities.UsageIdentity{
		AuthType:     entities.UsageIdentityAuthTypeAuthFile,
		AuthTypeName: "oauth",
		Identity:     authIndex,
		Type:         "codex",
		Provider:     "codex",
		Name:         authIndex,
	}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatalf("seed usage identity: %v", err)
	}
	return identity
}

func createCapacityObservation(t *testing.T, db *gorm.DB, usageIdentityID int64) entities.QuotaObservation {
	t.Helper()
	windowSeconds := int64(5 * time.Hour / time.Second)
	usedPercent := 10.0
	attributedTokens := int64(100)
	resetAt := time.Date(2099, 1, 1, 5, 0, 0, 0, time.UTC)
	observedAt := resetAt.Add(-time.Hour)
	observation := entities.QuotaObservation{
		UsageIdentityID:     usageIdentityID,
		AuthType:            "oauth",
		AuthIndex:           "auth-data",
		Provider:            "codex",
		WindowKindID:        estimate.WindowKindCodexFiveHour,
		QuotaKey:            "rate_limit.primary_window",
		Scope:               "window",
		WindowRole:          "primary",
		WindowSeconds:       &windowSeconds,
		ObservedAt:          observedAt,
		Source:              "manual",
		UsedPercent:         &usedPercent,
		PercentSource:       "reported",
		ResetAt:             &resetAt,
		AttributedTokens:    &attributedTokens,
		PricingSnapshotHash: "hash",
		CreatedAt:           observedAt,
	}
	if err := db.Create(&observation).Error; err != nil {
		t.Fatalf("seed quota observation: %v", err)
	}
	return observation
}

func capacityStubEstimates(observationID int64) []estimate.WindowEstimate {
	currentReset := time.Now().UTC().Add(4 * time.Hour)
	result := make([]estimate.WindowEstimate, 0, 11)
	for index := 0; index < 11; index++ {
		resetAt := currentReset.Add(-time.Duration(index) * 5 * time.Hour)
		result = append(result, estimate.WindowEstimate{
			Provider:      "codex",
			WindowKindID:  estimate.WindowKindCodexFiveHour,
			WindowSeconds: int64(5 * time.Hour / time.Second),
			EpochResetAt:  timePointer(resetAt),
			Confidence:    estimate.ConfidenceInsufficient,
			Points: []estimate.PointDiagnostic{{
				ObservationID: observationID,
				Class:         estimate.PointIncluded,
			}},
			FittedSeries: []estimate.FittedPoint{{
				ObservationID:       observationID,
				AttributedTokens:    100,
				RawUsedPercent:      10,
				AdjustedUsedPercent: 10,
				FittedPercent:       10,
			}},
			Method: estimate.MethodOLSBlockBootstrap,
		})
	}
	return result
}

func timePointer(value time.Time) *time.Time {
	return &value
}
