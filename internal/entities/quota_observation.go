package entities

import "time"

// QuotaObservation 是一次 provider quota row 的不可变采样事实。
type QuotaObservation struct {
	ID              int64   `gorm:"primaryKey" json:"id"`
	UsageIdentityID int64   `gorm:"not null;index:idx_quota_observations_identity_window_time,priority:1" json:"usage_identity_id"`
	AuthType        string  `gorm:"not null" json:"auth_type"`
	AuthIndex       string  `gorm:"not null" json:"auth_index"`
	AccountID       *string `json:"account_id"`
	PlanType        *string `json:"plan_type"`

	Provider      string    `gorm:"not null" json:"provider"`
	WindowKindID  string    `gorm:"not null;index:idx_quota_observations_identity_window_time,priority:2" json:"window_kind_id"`
	QuotaKey      string    `gorm:"not null" json:"quota_key"`
	Scope         string    `gorm:"not null" json:"scope"`
	GroupKey      string    `gorm:"not null" json:"group_key"`
	WindowRole    string    `gorm:"not null" json:"window_role"`
	WindowSeconds *int64    `json:"window_seconds"`
	ObservedAt    time.Time `gorm:"serializer:sortableTime;not null;index:idx_quota_observations_identity_window_time,priority:3;index:idx_quota_observations_observed_at" json:"observed_at"`
	Source        string    `gorm:"not null" json:"source"`

	UsedPercent       *float64 `json:"used_percent"`
	PercentSource     string   `gorm:"not null" json:"percent_source"`
	RemainingFraction *float64 `json:"remaining_fraction"`
	Used              *float64 `json:"used"`
	LimitValue        *float64 `json:"limit_value"`
	Remaining         *float64 `json:"remaining"`

	// ResetAt is compared in Go and is not used in SQLite ordering or range predicates, so storageTime remains sufficient.
	ResetAt           *time.Time `gorm:"serializer:storageTime" json:"reset_at"`
	ResetRaw          *string    `json:"reset_raw"`
	ResetAfterSeconds *int64     `json:"reset_after_seconds"`

	ProviderWindowTokens *int64   `json:"provider_window_tokens"`
	ProviderWindowCost   *float64 `json:"provider_window_cost"`

	AttributedTokens              *int64   `json:"attributed_tokens"`
	AttributedInputTokens         *int64   `json:"attributed_input_tokens"`
	AttributedOutputTokens        *int64   `json:"attributed_output_tokens"`
	AttributedCacheReadTokens     *int64   `json:"attributed_cache_read_tokens"`
	AttributedCacheCreationTokens *int64   `json:"attributed_cache_creation_tokens"`
	AttributedCostUSD             *float64 `json:"attributed_cost_usd"`
	AttributedCostComplete        bool     `gorm:"not null;default:false" json:"attributed_cost_complete"`
	TriggeringEventKey            *string  `json:"triggering_event_key"`
	PricingSnapshotHash           string   `gorm:"not null" json:"pricing_snapshot_hash"`

	// CreatedAt is write-audit metadata and is not used in SQLite ordering or range predicates, so storageTime remains sufficient.
	CreatedAt time.Time `gorm:"serializer:storageTime;not null" json:"created_at"`
}
