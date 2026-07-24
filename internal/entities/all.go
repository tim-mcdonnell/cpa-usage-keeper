package entities

// All 返回需要 AutoMigrate 的核心数据库实体列表。
func All() []any {
	return []any{
		&UsageEvent{},
		&RedisUsageInbox{},
		&ModelPriceSetting{},
		&ModelPriceRule{},
		&UsageIdentity{},
		&CPAAPIKey{},
		&UsageOverviewHourlyStat{},
		&UsageOverviewDailyStat{},
		&UsageOverviewAggregationCheckpoint{},
		// Activity 统计与独立 checkpoint 必须随全新数据库直接创建。
		&UsageActivityStat{},
		&UsageActivityAggregationCheckpoint{},
		&AuthSession{},
		&AppSetting{},
		&QuotaObservation{},
	}
}
