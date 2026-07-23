package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/timeutil"

	"gorm.io/gorm"
)

type QuotaAttributedUsage struct {
	TotalTokens         int64
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	CostUSD             float64
	CostComplete        bool
	PricingSnapshotHash string
}

type QuotaObservationInsertResult string

const (
	QuotaObservationInserted   QuotaObservationInsertResult = "inserted"
	QuotaObservationSkipped    QuotaObservationInsertResult = "skipped"
	QuotaObservationDailyLimit QuotaObservationInsertResult = "daily_limit"
)

// MaxUsageEventIDForCredential 使用 auth type 和 credential 的复合索引读取 cheap-gate watermark。
func MaxUsageEventIDForCredential(ctx context.Context, db *gorm.DB, authType string, authIndex string) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("database is nil")
	}
	var maxID int64
	err := db.WithContext(contextOrBackground(ctx)).
		Model(&entities.UsageEvent{}).
		Select("COALESCE(MAX(id), 0)").
		Where("auth_type = ? AND auth_index = ?", strings.TrimSpace(authType), strings.TrimSpace(authIndex)).
		Scan(&maxID).Error
	if err != nil {
		return 0, fmt.Errorf("load quota observation usage event watermark: %w", err)
	}
	return maxID, nil
}

// SumQuotaAttributedUsage 只扫描 raw events，并按半开窗口和 credential 两个身份字段聚合。
func SumQuotaAttributedUsage(
	ctx context.Context,
	db *gorm.DB,
	authType string,
	authIndex string,
	start time.Time,
	end time.Time,
	resolver pricing.Resolver,
) (QuotaAttributedUsage, error) {
	result := QuotaAttributedUsage{
		CostComplete:        true,
		PricingSnapshotHash: resolver.ContentHash(),
	}
	if db == nil {
		return QuotaAttributedUsage{}, fmt.Errorf("database is nil")
	}
	authType = strings.TrimSpace(authType)
	authIndex = strings.TrimSpace(authIndex)
	if authType == "" || authIndex == "" {
		return QuotaAttributedUsage{}, fmt.Errorf("auth_type and auth_index are required")
	}
	start = timeutil.NormalizeStorageTime(start)
	end = timeutil.NormalizeStorageTime(end)
	if start.IsZero() || end.IsZero() || !start.Before(end) {
		return result, nil
	}

	activeFields := resolver.ActiveFields()
	dimensions := UsagePricingDimensionColumns(activeFields)
	groupDimensions := append([]string{"model_alias", "model"}, dimensions[2:]...)
	selectDimensions := append([]string(nil), dimensions...)
	for index := range selectDimensions {
		if selectDimensions[index] == "model_alias" {
			selectDimensions[index] = "COALESCE(model_alias, '') AS model_alias"
		}
	}
	selectClause := strings.Join(selectDimensions, ", ") +
		", COALESCE(SUM(total_tokens), 0) AS total_tokens" +
		", COALESCE(SUM(input_tokens), 0) AS input_tokens" +
		", COALESCE(SUM(output_tokens), 0) AS output_tokens" +
		", COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens" +
		", COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens"

	var rows []usageWindowTokenStats
	err := db.WithContext(contextOrBackground(ctx)).
		Model(&entities.UsageEvent{}).
		Select(selectClause).
		Where(
			"auth_type = ? AND auth_index = ? AND timestamp >= ? AND timestamp < ?",
			authType,
			authIndex,
			timeutil.FormatStorageTime(start),
			timeutil.FormatStorageTime(end),
		).
		Group(strings.Join(groupDimensions, ", ")).
		Scan(&rows).Error
	if err != nil {
		return QuotaAttributedUsage{}, fmt.Errorf("sum quota attributed usage: %w", err)
	}

	for _, row := range rows {
		result.TotalTokens += row.TotalTokens
		result.InputTokens += row.InputTokens
		result.OutputTokens += row.OutputTokens
		result.CacheReadTokens += row.CacheReadTokens
		result.CacheCreationTokens += row.CacheCreationTokens
		cost := resolver.Calculate(newUsagePricingCostSubject(
			row.APIGroupKey,
			row.Model,
			authIndex,
			row.ModelAlias,
			row.ServiceTier,
			row.ResponseServiceTier,
			row.ReasoningEffort,
			row.Endpoint,
			row.ExecutorType,
			row.InputTokens,
			row.OutputTokens,
			row.CacheReadTokens,
			row.CacheCreationTokens,
		))
		result.CostUSD += cost.Cost.TotalCostUSD
		if !cost.Available {
			result.CostComplete = false
		}
	}
	return result, nil
}

// InsertQuotaObservationIfDue 在一个短 writer transaction 内确认 spacing、daily cap 并 append。
func InsertQuotaObservationIfDue(
	ctx context.Context,
	db *gorm.DB,
	observation entities.QuotaObservation,
	minimumSpacing time.Duration,
	dailyLimit int64,
) (QuotaObservationInsertResult, error) {
	if db == nil {
		return "", fmt.Errorf("database is nil")
	}
	result := QuotaObservationSkipped
	err := db.WithContext(contextOrBackground(ctx)).Transaction(func(tx *gorm.DB) error {
		var latest entities.QuotaObservation
		err := tx.
			Where("usage_identity_id = ? AND window_kind_id = ?", observation.UsageIdentityID, observation.WindowKindID).
			Order("observed_at DESC, id DESC").
			Take(&latest).Error
		switch {
		case err == nil:
			if !quotaObservationResetChanged(latest, observation) {
				if !observation.ObservedAt.After(latest.ObservedAt) || observation.ObservedAt.Sub(latest.ObservedAt) < minimumSpacing {
					result = QuotaObservationSkipped
					return nil
				}
			}
		case errors.Is(err, gorm.ErrRecordNotFound):
		default:
			return fmt.Errorf("load latest quota observation: %w", err)
		}

		if dailyLimit > 0 {
			dayStart, dayEnd := quotaObservationDayBounds(observation.ObservedAt)
			var count int64
			if err := tx.Model(&entities.QuotaObservation{}).
				Where(
					"usage_identity_id = ? AND window_kind_id = ? AND observed_at >= ? AND observed_at < ?",
					observation.UsageIdentityID,
					observation.WindowKindID,
					timeutil.FormatStorageTime(dayStart),
					timeutil.FormatStorageTime(dayEnd),
				).
				Count(&count).Error; err != nil {
				return fmt.Errorf("count daily quota observations: %w", err)
			}
			if count >= dailyLimit {
				result = QuotaObservationDailyLimit
				return nil
			}
		}

		if err := tx.Create(&observation).Error; err != nil {
			return fmt.Errorf("insert quota observation: %w", err)
		}
		result = QuotaObservationInserted
		return nil
	})
	return result, err
}

func ListQuotaObservations(
	ctx context.Context,
	db *gorm.DB,
	usageIdentityID int64,
	windowKindID string,
	start time.Time,
	end time.Time,
	limit int,
) ([]entities.QuotaObservation, bool, error) {
	if db == nil {
		return nil, false, fmt.Errorf("database is nil")
	}
	if limit <= 0 {
		return []entities.QuotaObservation{}, false, nil
	}
	var rows []entities.QuotaObservation
	err := db.WithContext(contextOrBackground(ctx)).
		Where(
			"usage_identity_id = ? AND window_kind_id = ? AND observed_at >= ? AND observed_at < ?",
			usageIdentityID,
			strings.TrimSpace(windowKindID),
			timeutil.FormatStorageTime(start),
			timeutil.FormatStorageTime(end),
		).
		Order("observed_at ASC, id ASC").
		Limit(limit + 1).
		Find(&rows).Error
	if err != nil {
		return nil, false, fmt.Errorf("list quota observations: %w", err)
	}
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	return rows, truncated, nil
}

func quotaObservationResetChanged(previous entities.QuotaObservation, candidate entities.QuotaObservation) bool {
	if previous.ResetAt != nil || candidate.ResetAt != nil {
		if previous.ResetAt == nil || candidate.ResetAt == nil {
			return true
		}
		return !previous.ResetAt.Equal(*candidate.ResetAt)
	}
	if !equalStringPointers(previous.ResetRaw, candidate.ResetRaw) {
		return true
	}
	return !equalInt64Pointers(previous.ResetAfterSeconds, candidate.ResetAfterSeconds)
}

func quotaObservationDayBounds(value time.Time) (time.Time, time.Time) {
	value = timeutil.NormalizeStorageTime(value)
	year, month, day := value.Date()
	start := time.Date(year, month, day, 0, 0, 0, 0, value.Location())
	return start, start.AddDate(0, 0, 1)
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

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
