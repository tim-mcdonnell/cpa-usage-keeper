package quota

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"cpa-usage-keeper/internal/timeutil"
)

const (
	quotaWindowFiveHourSeconds     int64 = 5 * 60 * 60
	quotaWindowSevenDaySeconds     int64 = 7 * 24 * 60 * 60
	quotaWindowThirtyDaySeconds    int64 = 30 * 24 * 60 * 60
	quotaWindowAverageMonthSeconds int64 = 365 * 24 * 60 * 60 / 12

	QuotaPercentSourceReported          = "reported"
	QuotaPercentSourceRemainingFraction = "from_remaining_fraction"
	QuotaPercentSourceUsedLimit         = "from_used_limit"
)

func NormalizeQuotaRows(output ProviderOutput) []QuotaRow {
	// 不在 provider 层强行统一原始结构，只在出口处转换为前端展示需要的 quota rows。
	var rows []QuotaRow
	switch result := output.Result.(type) {
	case AntigravityResult:
		rows = normalizeAntigravityQuotaRows(result)
	case *AntigravityResult:
		if result == nil {
			return nil
		}
		rows = normalizeAntigravityQuotaRows(*result)
	case CodexResult:
		rows = normalizeCodexQuotaRows(result)
	case *CodexResult:
		if result == nil {
			return nil
		}
		rows = normalizeCodexQuotaRows(*result)
	case GeminiCLIResult:
		rows = normalizeGeminiCLIQuotaRows(result)
	case *GeminiCLIResult:
		if result == nil {
			return nil
		}
		rows = normalizeGeminiCLIQuotaRows(*result)
	case ClaudeResult:
		rows = normalizeClaudeQuotaRows(result)
	case *ClaudeResult:
		if result == nil {
			return nil
		}
		rows = normalizeClaudeQuotaRows(*result)
	case KimiResult:
		rows = normalizeKimiQuotaRows(result)
	case *KimiResult:
		if result == nil {
			return nil
		}
		rows = normalizeKimiQuotaRows(*result)
	case XAIResult:
		rows = normalizeXAIQuotaRows(result)
	case *XAIResult:
		if result == nil {
			return nil
		}
		rows = normalizeXAIQuotaRows(*result)
	default:
		return nil
	}
	return attachQuotaObservationProvenance(rows)
}

func attachQuotaObservationProvenance(rows []QuotaRow) []QuotaRow {
	for index := range rows {
		row := &rows[index]
		if row.ResetRaw == "" {
			row.ResetRaw = row.ResetAt
		}
		switch {
		case row.UsedPercent != nil && row.PercentSource == "":
			row.PercentSource = QuotaPercentSourceReported
		case row.UsedPercent == nil && row.RemainingFraction != nil:
			usedPercent := (1 - *row.RemainingFraction) * 100
			row.UsedPercent = &usedPercent
			row.PercentSource = QuotaPercentSourceRemainingFraction
		case row.UsedPercent == nil && row.Used != nil && row.Limit != nil && *row.Limit > 0:
			usedPercent := *row.Used / *row.Limit * 100
			row.UsedPercent = &usedPercent
			row.PercentSource = QuotaPercentSourceUsedLimit
		}
	}
	return rows
}

func normalizeClaudeQuotaRows(result ClaudeResult) []QuotaRow {
	if result.Usage == nil {
		return nil
	}
	rows := make([]QuotaRow, 0, 8)
	rows = appendClaudeWindowQuotaRow(rows, "five_hour", "5h", "window", result.Usage.FiveHour)
	rows = appendClaudeWindowQuotaRow(rows, "seven_day", "Weekly", "window", result.Usage.SevenDay)
	rows = appendClaudeWindowQuotaRow(rows, "seven_day_oauth_apps", "7d OAuth Apps", "window", result.Usage.SevenDayOAuthApps)
	rows = appendClaudeWindowQuotaRow(rows, "seven_day_opus", "7d Opus", "model", result.Usage.SevenDayOpus)
	rows = appendClaudeWindowQuotaRow(rows, "seven_day_sonnet", "7d Sonnet", "model", result.Usage.SevenDaySonnet)
	rows = appendClaudeWindowQuotaRow(rows, "seven_day_cowork", "7d Cowork", "window", result.Usage.SevenDayCowork)
	rows = appendClaudeWindowQuotaRow(rows, "iguana_necktie", "Iguana Necktie", "window", result.Usage.IguanaNecktie)
	if result.Usage.ExtraUsage != nil {
		extra := result.Usage.ExtraUsage
		row := QuotaRow{
			Key:           "extra_usage",
			StableLimitID: "extra_usage",
			Label:         "Extra Usage",
			Scope:         "extra_usage",
			UsedPercent:   extra.Utilization,
			Allowed:       boolPtr(extra.IsEnabled),
		}
		if !extra.rawPresenceKnown || extra.usedCreditsPresent {
			row.Used = floatPtr(extra.UsedCredits)
		}
		if !extra.rawPresenceKnown || extra.monthlyLimitPresent {
			row.Limit = floatPtr(extra.MonthlyLimit)
		}
		rows = append(rows, row)
	}
	return rows
}

func appendClaudeWindowQuotaRow(rows []QuotaRow, key string, label string, scope string, window *ClaudeUsageWindow) []QuotaRow {
	if window == nil {
		return rows
	}
	var usedPercent *float64
	percentSource := ""
	if !window.rawPresenceKnown || window.utilizationPresent {
		usedPercent = floatPtr(window.Utilization)
		percentSource = QuotaPercentSourceReported
	}
	resetRaw := window.ResetsAt
	if window.rawPresenceKnown {
		resetRaw = window.resetsAtRaw
	}
	row := QuotaRow{
		Key:           key,
		StableLimitID: key,
		Label:         label,
		Scope:         scope,
		UsedPercent:   usedPercent,
		ResetAt:       window.ResetsAt,
		ResetRaw:      resetRaw,
		PercentSource: percentSource,
	}
	// Claude 只给官方语义明确的 5h 会话窗口和 seven_day 系列补 seconds，其它未知 key 不猜测。
	if key == "five_hour" {
		row.Window = &QuotaWindow{Seconds: intPtr(quotaWindowFiveHourSeconds)}
	} else if strings.HasPrefix(key, "seven_day") {
		row.Window = &QuotaWindow{Seconds: intPtr(quotaWindowSevenDaySeconds)}
	}
	return append(rows, row)
}

func normalizeCodexQuotaRows(result CodexResult) []QuotaRow {
	// Codex 根据 limit_window_seconds 明确区分 5h/Weekly/Monthly；未知窗口回退到 primary/secondary 角色。
	if result.Usage == nil {
		return nil
	}
	rows := make([]QuotaRow, 0, 4+len(result.Usage.AdditionalRateLimits)*2)
	rows = appendCodexWindowQuotaRows(rows, "rate_limit", "5h", "Weekly", "window", "", result.Usage.RateLimit)
	rows = appendCodexWindowQuotaRows(rows, "code_review_rate_limit", "Code Review 5h", "Code Review Weekly", "code_review", "", result.Usage.CodeReviewRateLimit)
	for _, additional := range result.Usage.AdditionalRateLimits {
		// 主限额之外的 code review / spark 等窗口也保留为 extra quota，避免丢失上游数据。
		metric := additional.MeteredFeature
		if metric == "" {
			metric = additional.LimitName
		}
		primaryLabel := additional.LimitName + " 5h"
		secondaryLabel := additional.LimitName + " Weekly"
		rows = appendCodexWindowQuotaRows(rows, "additional_rate_limits."+additional.LimitName, primaryLabel, secondaryLabel, "additional", metric, additional.RateLimit)
	}
	return rows
}

func appendCodexWindowQuotaRows(rows []QuotaRow, keyPrefix string, primaryLabel string, secondaryLabel string, scope string, metric string, info *CodexRateLimitInfo) []QuotaRow {
	if info == nil {
		return rows
	}
	rows = appendCodexWindowQuotaRow(rows, keyPrefix+".primary_window", primaryLabel, scope, metric, info, info.PrimaryWindow)
	rows = appendCodexWindowQuotaRow(rows, keyPrefix+".secondary_window", secondaryLabel, scope, metric, info, info.SecondaryWindow)
	return rows
}

func codexWindowLabel(key string, label string, seconds int64) string {
	switch seconds {
	case quotaWindowFiveHourSeconds:
		return codexKnownWindowLabel(label, "5h")
	case quotaWindowSevenDaySeconds:
		return codexKnownWindowLabel(label, "Weekly")
	case quotaWindowThirtyDaySeconds, quotaWindowAverageMonthSeconds:
		return codexKnownWindowLabel(label, "Monthly")
	}
	return codexRoleWindowLabel(key, label)
}

func codexKnownWindowLabel(label string, replacement string) string {
	if label == "5h" || label == "Weekly" || label == "Monthly" || label == "Window" {
		return replacement
	}
	for _, candidate := range []string{"5h", "Weekly", "Monthly", "Window"} {
		if strings.Contains(label, candidate) {
			return strings.Replace(label, candidate, replacement, 1)
		}
	}
	return label
}

func codexRoleWindowLabel(key string, label string) string {
	role := codexWindowRole(key)
	if role == "" {
		return label
	}
	fallback := codexPrefixedWindowRole(key, role)
	if label == "" || label == "5h" || label == "Weekly" || label == "Monthly" || label == "Window" {
		return fallback
	}
	for _, candidate := range []string{"5h", "Weekly", "Monthly", "Window"} {
		if strings.Contains(label, candidate) {
			return strings.Replace(label, candidate, role, 1)
		}
	}
	return fallback
}

func codexWindowRole(key string) string {
	if strings.HasSuffix(key, ".primary_window") {
		return "Primary"
	}
	if strings.HasSuffix(key, ".secondary_window") {
		return "Secondary"
	}
	return ""
}

func codexPrefixedWindowRole(key string, role string) string {
	if strings.HasPrefix(key, "code_review_rate_limit.") {
		return "Code Review " + role
	}
	if strings.HasPrefix(key, "additional_rate_limits.") {
		name := strings.TrimPrefix(key, "additional_rate_limits.")
		name = strings.TrimSuffix(name, ".primary_window")
		name = strings.TrimSuffix(name, ".secondary_window")
		if name != "" {
			return name + " " + role
		}
	}
	return role
}

func appendCodexWindowQuotaRow(rows []QuotaRow, key string, label string, scope string, metric string, info *CodexRateLimitInfo, window *CodexUsageWindow) []QuotaRow {
	// 每个窗口单独展开为一条 quota row，前端再按窗口秒数决定主/次展示位置。
	if window == nil {
		return rows
	}
	label = codexWindowLabel(key, label, window.LimitWindowSeconds)
	var usedPercent *float64
	percentSource := ""
	if !window.rawPresenceKnown || window.usedPercentPresent {
		usedPercent = floatPtr(window.UsedPercent)
		percentSource = QuotaPercentSourceReported
	}
	row := QuotaRow{
		Key:               key,
		StableLimitID:     codexStableLimitID(key),
		Label:             label,
		Scope:             scope,
		Metric:            metric,
		UsedPercent:       usedPercent,
		Allowed:           info.Allowed,
		LimitReached:      info.LimitReached,
		WindowUsageTokens: window.WindowUsageTokens,
		WindowUsageCost:   window.WindowUsageCost,
		PercentSource:     percentSource,
		WindowRole:        strings.ToLower(codexWindowRole(key)),
	}
	if window.LimitWindowSeconds != 0 || (window.rawPresenceKnown && window.limitWindowSecondsPresent) {
		row.Window = &QuotaWindow{Seconds: intPtr(window.LimitWindowSeconds)}
	}
	if window.ResetAfterSeconds != 0 || (window.rawPresenceKnown && window.resetAfterSecondsPresent) {
		row.ResetAfterSeconds = intPtr(window.ResetAfterSeconds)
	}
	if window.ResetAt != 0 || (window.rawPresenceKnown && window.resetAtPresent) {
		row.ResetAt = timeutil.FormatStorageTime(time.Unix(window.ResetAt, 0))
		row.ResetRaw = window.resetAtRaw
		if !window.rawPresenceKnown {
			row.ResetRaw = strconv.FormatInt(window.ResetAt, 10)
		}
	}
	return append(rows, row)
}

func codexStableLimitID(key string) string {
	key = strings.TrimSuffix(key, ".primary_window")
	key = strings.TrimSuffix(key, ".secondary_window")
	return strings.TrimPrefix(key, "additional_rate_limits.")
}

func normalizeGeminiCLIQuotaRows(result GeminiCLIResult) []QuotaRow {
	// Gemini CLI 同时可能返回模型桶和 Code Assist credits，两类都平铺给前端。
	rows := make([]QuotaRow, 0)
	if result.Quota != nil {
		for _, bucket := range result.Quota.Buckets {
			var remaining *float64
			if !bucket.rawPresenceKnown || bucket.remainingAmountPresent {
				remaining = floatPtr(bucket.RemainingAmount)
			}
			var remainingFraction *float64
			if !bucket.rawPresenceKnown || bucket.remainingFractionPresent {
				remainingFraction = floatPtr(bucket.RemainingFraction)
			}
			resetRaw := bucket.ResetTime
			if bucket.rawPresenceKnown {
				resetRaw = bucket.resetTimeRaw
			}
			rows = append(rows, QuotaRow{
				Key:               "bucket." + bucket.ModelID + "." + bucket.TokenType,
				StableLimitID:     bucket.ModelID + "." + bucket.TokenType,
				Label:             bucket.ModelID,
				Scope:             "model",
				Metric:            bucket.TokenType,
				Remaining:         remaining,
				RemainingFraction: remainingFraction,
				ResetAt:           bucket.ResetTime,
				ResetRaw:          resetRaw,
			})
		}
	}
	if result.CodeAssist != nil {
		rows = appendGeminiCLICredits(rows, "code_assist.current_tier", result.CodeAssist.CurrentTier)
		rows = appendGeminiCLICredits(rows, "code_assist.paid_tier", result.CodeAssist.PaidTier)
	}
	return rows
}

func appendGeminiCLICredits(rows []QuotaRow, keyPrefix string, tier *GeminiCliUserTier) []QuotaRow {
	if tier == nil {
		return rows
	}
	for _, credit := range tier.AvailableCredits {
		stableTierID := firstNonEmpty(tier.ID, keyPrefix)
		var remaining *float64
		if !credit.rawPresenceKnown || credit.creditAmountPresent {
			remaining = floatPtr(credit.CreditAmount)
		}
		rows = append(rows, QuotaRow{
			Key:           keyPrefix + "." + credit.CreditType,
			StableLimitID: stableTierID + "." + credit.CreditType,
			Label:         "Code Assist Credit",
			Scope:         "credits",
			Metric:        credit.CreditType,
			Remaining:     remaining,
		})
	}
	return rows
}

func normalizeAntigravityQuotaRows(result AntigravityResult) []QuotaRow {
	if result.Quota == nil {
		return nil
	}
	rows := make([]QuotaRow, 0)
	for groupIndex, group := range result.Quota.Groups {
		groupKey := antigravityQuotaGroupKey(group.DisplayName, groupIndex)
		groupLabel := firstNonEmpty(group.DisplayName, fmt.Sprintf("Quota Group %d", groupIndex+1))
		groupRows := make([]QuotaRow, 0, len(group.Buckets))
		for bucketIndex, bucket := range group.Buckets {
			if bucket.RemainingFraction == nil {
				continue
			}
			label, metric, window := normalizeAntigravityQuotaWindow(bucket)
			bucketKey := firstNonEmpty(bucket.BucketID, fmt.Sprintf("group-%d-bucket-%d", groupIndex+1, bucketIndex+1))
			row := QuotaRow{
				Key:               "bucket." + groupKey + "." + bucketKey,
				StableLimitID:     bucket.BucketID,
				Label:             label,
				Scope:             "quota_group",
				Metric:            metric,
				GroupKey:          groupKey,
				GroupLabel:        groupLabel,
				GroupDescription:  group.Description,
				RemainingFraction: bucket.RemainingFraction,
				Window:            window,
				ResetAt:           bucket.ResetTime,
				ResetRaw:          bucket.resetTimeRaw,
			}
			if row.ResetRaw == "" {
				row.ResetRaw = bucket.ResetTime
			}
			groupRows = append(groupRows, row)
		}
		sort.SliceStable(groupRows, func(i, j int) bool {
			return antigravityQuotaWindowOrder(groupRows[i].Metric) < antigravityQuotaWindowOrder(groupRows[j].Metric)
		})
		rows = append(rows, groupRows...)
	}
	return rows
}

func antigravityQuotaGroupKey(displayName string, groupIndex int) string {
	var normalized strings.Builder
	pendingSeparator := false
	for _, value := range strings.ToLower(strings.TrimSpace(displayName)) {
		if (value >= 'a' && value <= 'z') || (value >= '0' && value <= '9') {
			if pendingSeparator && normalized.Len() > 0 {
				normalized.WriteByte('-')
			}
			normalized.WriteRune(value)
			pendingSeparator = false
			continue
		}
		if normalized.Len() > 0 {
			pendingSeparator = true
		}
	}
	if normalized.Len() == 0 {
		return fmt.Sprintf("antigravity-group-%d", groupIndex+1)
	}
	return "antigravity-" + normalized.String()
}

func normalizeAntigravityQuotaWindow(bucket AntigravityQuotaBucket) (string, string, *QuotaWindow) {
	window := strings.ToLower(strings.TrimSpace(bucket.Window))
	switch window {
	case "5h", "five-hour", "five_hour":
		return "5h", "5h", &QuotaWindow{Seconds: intPtr(quotaWindowFiveHourSeconds)}
	case "weekly", "week":
		return "Weekly", "weekly", &QuotaWindow{Seconds: intPtr(quotaWindowSevenDaySeconds)}
	default:
		return firstNonEmpty(bucket.DisplayName, bucket.BucketID, "Quota"), window, nil
	}
}

func antigravityQuotaWindowOrder(metric string) int {
	switch metric {
	case "5h":
		return 0
	case "weekly":
		return 1
	default:
		return 2
	}
}

func normalizeKimiQuotaRows(result KimiResult) []QuotaRow {
	// Kimi 的短窗口位于 limits，顶层 usage 是较长的汇总窗口；按短窗口到长窗口输出。
	if result.Usage == nil {
		return nil
	}
	rows := make([]QuotaRow, 0, 1+len(result.Usage.Limits))
	for index, limit := range result.Usage.Limits {
		keyName := limit.Name
		if keyName == "" {
			keyName = fmt.Sprintf("%d", index)
		}
		window := kimiWindow(limit)
		label := kimiLimitLabel(limit, window)
		scope := firstNonEmpty(limit.Scope, "limit")
		used, quotaLimit, remaining := limit.Used, limit.Limit, limit.Remaining
		usedPresent := !limit.rawPresenceKnown || limit.usedPresent
		limitPresent := !limit.rawPresenceKnown || limit.limitPresent
		remainingPresent := !limit.rawPresenceKnown || limit.remainingPresent
		usedDerived := false
		// 新协议把额度值放在 detail；旧协议只把 reset 信息放在 detail，额度仍在外层。
		if hasKimiQuotaValues(limit.Detail) {
			used = limit.Detail.Used
			quotaLimit = limit.Detail.Limit
			remaining = limit.Detail.Remaining
			usedPresent = !limit.Detail.rawPresenceKnown || limit.Detail.usedPresent || limit.Detail.usedDerived
			limitPresent = !limit.Detail.rawPresenceKnown || limit.Detail.limitPresent
			remainingPresent = !limit.Detail.rawPresenceKnown || limit.Detail.remainingPresent
			usedDerived = limit.Detail.usedDerived
		}
		row := QuotaRow{
			Key:           "limits." + keyName,
			StableLimitID: limit.Name,
			Label:         label,
			Scope:         scope,
			Metric:        limit.Name,
			ResetAt:       firstNonEmpty(limit.ResetAt, resetAtFromKimiDetail(limit.Detail)),
		}
		if usedPresent {
			row.Used = floatPtr(used)
			row.UsedDerived = usedDerived
		}
		if limitPresent {
			row.Limit = floatPtr(quotaLimit)
		}
		if remainingPresent {
			row.Remaining = floatPtr(remaining)
		}
		row.ResetRaw = limit.ResetAt
		if limit.rawPresenceKnown {
			row.ResetRaw = limit.resetAtRaw
		}
		if row.Limit != nil && *row.Limit > 0 && row.Used != nil {
			row.UsedPercent = floatPtr(*row.Used / *row.Limit * 100)
			row.PercentSource = QuotaPercentSourceUsedLimit
		}
		if limit.ResetIn != 0 || (limit.rawPresenceKnown && limit.resetInPresent) {
			row.ResetAfterSeconds = intPtr(int64(limit.ResetIn))
		} else if limit.Detail != nil && (limit.Detail.ResetIn != 0 || (limit.Detail.rawPresenceKnown && limit.Detail.resetInPresent)) {
			row.ResetAfterSeconds = intPtr(int64(limit.Detail.ResetIn))
		}
		row.Window = window
		rows = append(rows, row)
	}
	if isMeaningfulKimiDetail(result.Usage.Usage) {
		rows = append(rows, kimiDetailQuotaRow("usage", "summary", "Weekly", result.Usage.Usage))
	}
	return rows
}

func normalizeXAIQuotaRows(result XAIResult) []QuotaRow {
	// xAI 两个 billing endpoint 同级返回，输出顺序固定为 Weekly、Monthly、PAYG、产品额度。
	weeklyConfig := xaiBillingConfig(result.Weekly)
	monthlyConfig := xaiBillingConfig(result.Monthly)
	rows := make([]QuotaRow, 0, 3)
	if row, ok := xaiWeeklyQuotaRow(weeklyConfig); ok {
		rows = append(rows, row)
	}
	rows = append(rows, xaiMonthlyQuotaRows(monthlyConfig)...)
	rows = append(rows, xaiProductQuotaRows(weeklyConfig)...)
	return rows
}

func xaiBillingConfig(payload *XAIBillingPayload) *XAIBillingConfig {
	if payload == nil {
		return nil
	}
	return payload.Config
}

func xaiWeeklyQuotaRow(config *XAIBillingConfig) (QuotaRow, bool) {
	if config == nil || config.CreditUsagePercent == nil {
		return QuotaRow{}, false
	}
	usedPercent := *config.CreditUsagePercent
	limitReached := usedPercent >= 100
	row := QuotaRow{
		Key:           xaiWeeklyBillingQuotaKey,
		StableLimitID: "weekly",
		Label:         "Weekly",
		Scope:         "billing",
		Metric:        "weekly",
		UsedPercent:   floatPtr(usedPercent),
		Allowed:       boolPtr(!limitReached),
		LimitReached:  boolPtr(limitReached),
		Window:        &QuotaWindow{Seconds: intPtr(quotaWindowSevenDaySeconds)},
		ResetAt:       xaiWeeklyResetAt(config),
	}
	row.ResetRaw = xaiWeeklyResetRaw(config)
	return row, true
}

func xaiMonthlyQuotaRows(config *XAIBillingConfig) []QuotaRow {
	if config == nil {
		return nil
	}
	monthlyLimit, hasMonthlyLimit := xaiMoneyValue(config.MonthlyLimit)
	totalUsed, hasTotalUsed := xaiMoneyValue(config.Used)
	onDemandCap, hasOnDemandCap := xaiMoneyValue(config.OnDemandCap)
	onDemandUsed, hasOnDemandUsed := xaiMoneyValue(config.OnDemandUsed)
	if !hasOnDemandUsed && hasTotalUsed && hasMonthlyLimit {
		onDemandUsed = math.Max(0, totalUsed-monthlyLimit)
		hasOnDemandUsed = true
	}

	rows := make([]QuotaRow, 0, 2)
	if hasMonthlyLimit || hasTotalUsed {
		row := QuotaRow{
			Key:           "billing.monthly",
			StableLimitID: "monthly",
			Label:         "Monthly Spend",
			Scope:         "billing",
			Metric:        "usd_cents",
			Window:        &QuotaWindow{Seconds: intPtr(quotaWindowThirtyDaySeconds)},
			ResetAt:       config.BillingPeriodEnd,
			ResetRaw:      firstNonEmpty(config.billingPeriodEndRaw, config.BillingPeriodEnd),
		}
		if hasMonthlyLimit {
			row.Limit = floatPtr(monthlyLimit)
		}
		if hasTotalUsed {
			includedUsed := totalUsed
			if hasMonthlyLimit {
				includedUsed = math.Min(totalUsed, math.Max(0, monthlyLimit))
			}
			row.Used = floatPtr(includedUsed)
			if hasMonthlyLimit {
				row.Remaining = floatPtr(math.Max(0, monthlyLimit-includedUsed))
				if monthlyLimit > 0 {
					row.UsedPercent = floatPtr(includedUsed / monthlyLimit * 100)
					row.PercentSource = QuotaPercentSourceUsedLimit
					onDemandAvailable := hasOnDemandCap && onDemandCap > 0 && hasOnDemandUsed && onDemandUsed < onDemandCap
					limitReached := includedUsed >= monthlyLimit && !onDemandAvailable
					row.LimitReached = boolPtr(limitReached)
					row.Allowed = boolPtr(!limitReached)
				}
			}
		}
		rows = append(rows, row)
	}

	if hasOnDemandCap && onDemandCap > 0 {
		row := QuotaRow{
			Key:           "billing.on_demand",
			StableLimitID: "on_demand",
			Label:         "Pay-as-you-go",
			Scope:         "billing",
			Metric:        "usd_cents",
			Limit:         floatPtr(onDemandCap),
			Window:        &QuotaWindow{Seconds: intPtr(quotaWindowThirtyDaySeconds)},
			ResetAt:       config.BillingPeriodEnd,
			ResetRaw:      firstNonEmpty(config.billingPeriodEndRaw, config.BillingPeriodEnd),
		}
		if hasOnDemandUsed {
			row.Used = floatPtr(onDemandUsed)
			row.Remaining = floatPtr(math.Max(0, onDemandCap-onDemandUsed))
			row.UsedPercent = floatPtr(onDemandUsed / onDemandCap * 100)
			row.PercentSource = QuotaPercentSourceUsedLimit
			limitReached := onDemandUsed >= onDemandCap
			row.LimitReached = boolPtr(limitReached)
			row.Allowed = boolPtr(!limitReached)
		}
		rows = append(rows, row)
	}
	return rows
}

func xaiProductQuotaRows(config *XAIBillingConfig) []QuotaRow {
	if config == nil || len(config.ProductUsage) == 0 {
		return nil
	}
	type productQuota struct {
		name           string
		normalizedName string
		usedPercent    float64
	}
	products := make(map[string]productQuota, len(config.ProductUsage))
	for _, item := range config.ProductUsage {
		name := strings.Join(strings.Fields(item.Product), " ")
		normalizedName := strings.ToLower(name)
		if normalizedName == "" || item.UsagePercent == nil {
			continue
		}
		if current, ok := products[normalizedName]; ok {
			if *item.UsagePercent > current.usedPercent {
				current.usedPercent = *item.UsagePercent
				products[normalizedName] = current
			}
			continue
		}
		products[normalizedName] = productQuota{name: name, normalizedName: normalizedName, usedPercent: *item.UsagePercent}
	}
	ordered := make([]productQuota, 0, len(products))
	for _, product := range products {
		ordered = append(ordered, product)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].normalizedName < ordered[j].normalizedName
	})
	rows := make([]QuotaRow, 0, len(ordered))
	for _, product := range ordered {
		limitReached := product.usedPercent >= 100
		row := QuotaRow{
			Key:           "billing.weekly.product." + url.QueryEscape(product.normalizedName),
			StableLimitID: product.normalizedName,
			Label:         product.name + " Usage",
			Scope:         "product",
			Metric:        product.name,
			UsedPercent:   floatPtr(product.usedPercent),
			Allowed:       boolPtr(!limitReached),
			LimitReached:  boolPtr(limitReached),
			Window:        &QuotaWindow{Seconds: intPtr(quotaWindowSevenDaySeconds)},
			ResetAt:       xaiWeeklyResetAt(config),
		}
		row.ResetRaw = xaiWeeklyResetRaw(config)
		rows = append(rows, row)
	}
	return rows
}

func xaiWeeklyResetAt(config *XAIBillingConfig) string {
	if config == nil {
		return ""
	}
	if config.CurrentPeriod != nil && strings.TrimSpace(config.CurrentPeriod.End) != "" {
		return config.CurrentPeriod.End
	}
	return config.BillingPeriodEnd
}

func xaiWeeklyResetRaw(config *XAIBillingConfig) string {
	if config == nil {
		return ""
	}
	if config.CurrentPeriod != nil && strings.TrimSpace(config.CurrentPeriod.End) != "" {
		return firstNonEmpty(config.CurrentPeriod.endRaw, config.CurrentPeriod.End)
	}
	return firstNonEmpty(config.billingPeriodEndRaw, config.BillingPeriodEnd)
}

func xaiMoneyValue(value XAIMoneyValue) (float64, bool) {
	if value.Val == nil {
		return 0, false
	}
	return *value.Val, true
}

func kimiDetailQuotaRow(key string, scope string, fallbackLabel string, detail *KimiUsageDetail) QuotaRow {
	row := QuotaRow{
		Key:           key,
		StableLimitID: firstNonEmpty(detail.Name, key),
		Label:         firstNonEmpty(detail.Title, fallbackLabel),
		Scope:         scope,
		Metric:        detail.Name,
		ResetAt:       detail.ResetAt,
	}
	if !detail.rawPresenceKnown || detail.usedPresent || detail.usedDerived {
		row.Used = floatPtr(detail.Used)
		row.UsedDerived = detail.usedDerived
	}
	if !detail.rawPresenceKnown || detail.limitPresent {
		row.Limit = floatPtr(detail.Limit)
	}
	if !detail.rawPresenceKnown || detail.remainingPresent {
		row.Remaining = floatPtr(detail.Remaining)
	}
	row.ResetRaw = detail.ResetAt
	if detail.rawPresenceKnown {
		row.ResetRaw = detail.resetAtRaw
	}
	if row.Limit != nil && *row.Limit > 0 && row.Used != nil {
		row.UsedPercent = floatPtr(detail.Used / detail.Limit * 100)
		row.PercentSource = QuotaPercentSourceUsedLimit
	}
	if detail.ResetIn != 0 || (detail.rawPresenceKnown && detail.resetInPresent) {
		row.ResetAfterSeconds = intPtr(int64(detail.ResetIn))
	}
	return row
}

func isMeaningfulKimiDetail(detail *KimiUsageDetail) bool {
	if detail == nil {
		return false
	}
	return detail.Used != 0 || detail.Limit != 0 || detail.Remaining != 0 ||
		detail.usedPresent || detail.limitPresent || detail.remainingPresent ||
		detail.Name != "" || detail.Title != "" || detail.ResetAt != "" ||
		detail.ResetIn != 0 || detail.resetInPresent || detail.TTL != 0
}

func hasKimiQuotaValues(detail *KimiUsageDetail) bool {
	return detail != nil && (detail.Used != 0 || detail.Limit != 0 || detail.Remaining != 0 ||
		detail.usedPresent || detail.usedDerived || detail.limitPresent || detail.remainingPresent)
}

func resetAtFromKimiDetail(detail *KimiUsageDetail) string {
	if detail == nil {
		return ""
	}
	return detail.ResetAt
}

func kimiWindow(limit KimiLimitItem) *QuotaWindow {
	if limit.Window != nil {
		return quotaWindowFromDurationUnit(limit.Window.Duration, limit.Window.TimeUnit)
	}
	if limit.Duration != 0 || limit.TimeUnit != "" {
		return quotaWindowFromDurationUnit(limit.Duration, limit.TimeUnit)
	}
	return nil
}

func kimiLimitLabel(limit KimiLimitItem, window *QuotaWindow) string {
	if label := firstNonEmpty(limit.Title, limit.Name); label != "" {
		return label
	}
	if window != nil && window.Seconds != nil {
		switch *window.Seconds {
		case quotaWindowFiveHourSeconds:
			return "5h"
		}
	}
	return "Limit"
}

func quotaWindowFromDurationUnit(duration int64, unit string) *QuotaWindow {
	window := &QuotaWindow{Duration: floatPtr(float64(duration)), Unit: unit}
	// Kimi 返回显式 duration/unit 时才换算 seconds；未知单位保留原字段但不参与窗口用量统计。
	normalizedUnit := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(unit)), "time_unit_")
	switch normalizedUnit {
	case "second", "seconds", "s":
		window.Seconds = intPtr(int64(duration))
	case "minute", "minutes", "m":
		window.Seconds = intPtr(int64(duration) * 60)
	case "hour", "hours", "h":
		window.Seconds = intPtr(int64(duration) * 3600)
	case "day", "days", "d":
		window.Seconds = intPtr(int64(duration) * 86400)
	case "week", "weeks", "w":
		window.Seconds = intPtr(int64(duration) * 604800)
	}
	return window
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func floatPtr(value float64) *float64 {
	return &value
}

func intPtr(value int64) *int64 {
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}
