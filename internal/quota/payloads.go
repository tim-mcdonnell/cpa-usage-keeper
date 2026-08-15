package quota

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"cpa-usage-keeper/internal/cpa/dto/apicall"
)

func parseAntigravityQuotaPayload(response *apicall.Response) (*AntigravityQuotaPayload, error) {
	object, err := parseResponseObject(response)
	if err != nil {
		return nil, err
	}
	if _, ok := object["groups"]; !ok {
		if nested := objectField(object, "body"); nested != nil {
			object = nested
		}
	}
	rawGroups, ok := object["groups"]
	if !ok {
		return nil, fmt.Errorf("antigravity quota response missing groups")
	}
	var groups []json.RawMessage
	if err := json.Unmarshal(rawGroups, &groups); err != nil {
		return nil, fmt.Errorf("antigravity quota response invalid groups: %w", err)
	}
	payload := &AntigravityQuotaPayload{}
	for _, rawGroup := range groups {
		groupObject := rawObject(rawGroup)
		if groupObject == nil {
			continue
		}
		group := AntigravityQuotaGroup{
			DisplayName: stringField(groupObject, "displayName", "display_name"),
			Description: stringField(groupObject, "description"),
		}
		for _, rawBucket := range arrayField(groupObject, "buckets") {
			bucketObject := rawObject(rawBucket)
			if bucketObject == nil {
				continue
			}
			remainingFraction := quotaFractionPtrField(bucketObject, "remainingFraction", "remaining_fraction")
			if remainingFraction == nil {
				continue
			}
			group.Buckets = append(group.Buckets, AntigravityQuotaBucket{
				BucketID:          stringField(bucketObject, "bucketId", "bucket_id"),
				DisplayName:       stringField(bucketObject, "displayName", "display_name"),
				Window:            stringField(bucketObject, "window"),
				RemainingFraction: remainingFraction,
				ResetTime:         stringField(bucketObject, "resetTime", "reset_time"),
				resetTimeRaw:      rawScalarField(bucketObject, "resetTime", "reset_time"),
			})
		}
		if len(group.Buckets) > 0 {
			payload.Groups = append(payload.Groups, group)
		}
	}
	return payload, nil
}

func parseAntigravitySubscriptionPayload(response *apicall.Response) (*AntigravitySubscriptionPayload, error) {
	object, err := parseResponseObject(response)
	if err != nil {
		return nil, err
	}
	if nested := objectField(object, "body"); nested != nil {
		object = nested
	}
	return &AntigravitySubscriptionPayload{
		CurrentTier: parseGeminiCliUserTier(objectField(object, "currentTier", "current_tier")),
		PaidTier:    parseGeminiCliUserTier(objectField(object, "paidTier", "paid_tier")),
	}, nil
}

func parseCodexUsagePayload(response *apicall.Response) (*CodexUsagePayload, error) {
	object, err := parseResponseObject(response)
	if err != nil {
		return nil, err
	}
	payload := &CodexUsagePayload{
		PlanType:              stringField(object, "plan_type", "planType"),
		RateLimit:             parseCodexRateLimitInfo(objectField(object, "rate_limit", "rateLimit")),
		CodeReviewRateLimit:   parseCodexRateLimitInfo(objectField(object, "code_review_rate_limit", "codeReviewRateLimit")),
		RateLimitResetCredits: parseCodexRateLimitResetCredits(objectField(object, "rate_limit_reset_credits", "rateLimitResetCredits")),
	}
	for _, raw := range arrayField(object, "additional_rate_limits", "additionalRateLimits") {
		limitObject := rawObject(raw)
		if limitObject == nil {
			continue
		}
		payload.AdditionalRateLimits = append(payload.AdditionalRateLimits, CodexAdditionalRateLimit{
			LimitName:      stringField(limitObject, "limit_name", "limitName"),
			MeteredFeature: stringField(limitObject, "metered_feature", "meteredFeature"),
			RateLimit:      parseCodexRateLimitInfo(objectField(limitObject, "rate_limit", "rateLimit")),
		})
	}
	return payload, nil
}

func parseCodexRateLimitResetCredits(object map[string]json.RawMessage) *CodexRateLimitResetCredits {
	if object == nil {
		return nil
	}
	// 官方刷新结果可能省略 reset credit 字段；只有明确返回 available_count 时才暴露给前端。
	availableCount := intPtrField(object, "available_count", "availableCount")
	if availableCount == nil {
		return nil
	}
	count := int(*availableCount)
	return &CodexRateLimitResetCredits{AvailableCount: &count}
}

func parseCodexRateLimitInfo(object map[string]json.RawMessage) *CodexRateLimitInfo {
	if object == nil {
		return nil
	}
	return &CodexRateLimitInfo{
		Allowed:         boolPtrField(object, "allowed"),
		LimitReached:    boolPtrField(object, "limit_reached", "limitReached"),
		PrimaryWindow:   parseCodexUsageWindow(objectField(object, "primary_window", "primaryWindow")),
		SecondaryWindow: parseCodexUsageWindow(objectField(object, "secondary_window", "secondaryWindow")),
	}
}

func parseCodexUsageWindow(object map[string]json.RawMessage) *CodexUsageWindow {
	if object == nil {
		return nil
	}
	window := &CodexUsageWindow{
		rawPresenceKnown:  true,
		WindowUsageTokens: intPtrField(object, "window_usage_tokens", "windowUsageTokens"),
		WindowUsageCost:   floatPtrField(object, "window_usage_cost", "windowUsageCost"),
	}
	if value := floatPtrField(object, "used_percent", "usedPercent"); value != nil {
		window.UsedPercent = *value
		window.usedPercentPresent = true
	}
	if value := intPtrField(object, "limit_window_seconds", "limitWindowSeconds"); value != nil {
		window.LimitWindowSeconds = *value
		window.limitWindowSecondsPresent = true
	}
	if value := intPtrField(object, "reset_after_seconds", "resetAfterSeconds"); value != nil {
		window.ResetAfterSeconds = *value
		window.resetAfterSecondsPresent = true
	}
	if value := intPtrField(object, "reset_at", "resetAt"); value != nil {
		window.ResetAt = *value
		window.resetAtPresent = true
		window.resetAtRaw = rawScalarField(object, "reset_at", "resetAt")
	}
	return window
}

func parseGeminiCliQuotaPayload(response *apicall.Response) (*GeminiCliQuotaPayload, error) {
	object, err := parseResponseObject(response)
	if err != nil {
		return nil, err
	}
	payload := &GeminiCliQuotaPayload{}
	for _, raw := range arrayField(object, "buckets") {
		bucketObject := rawObject(raw)
		if bucketObject == nil {
			continue
		}
		bucket := GeminiCliQuotaBucket{
			ModelID:          stringField(bucketObject, "modelId", "model_id"),
			TokenType:        stringField(bucketObject, "tokenType", "token_type"),
			ResetTime:        stringField(bucketObject, "resetTime", "reset_time"),
			rawPresenceKnown: true,
			resetTimeRaw:     rawScalarField(bucketObject, "resetTime", "reset_time"),
		}
		if value := floatPtrField(bucketObject, "remainingFraction", "remaining_fraction"); value != nil {
			bucket.RemainingFraction = *value
			bucket.remainingFractionPresent = true
		}
		if value := floatPtrField(bucketObject, "remainingAmount", "remaining_amount"); value != nil {
			bucket.RemainingAmount = *value
			bucket.remainingAmountPresent = true
		}
		payload.Buckets = append(payload.Buckets, bucket)
	}
	return payload, nil
}

func parseGeminiCliCodeAssistPayload(response *apicall.Response) (*GeminiCLICodeAssistPayload, error) {
	object, err := parseResponseObject(response)
	if err != nil {
		return nil, err
	}
	return &GeminiCLICodeAssistPayload{
		CurrentTier: parseGeminiCliUserTier(objectField(object, "currentTier", "current_tier")),
		PaidTier:    parseGeminiCliUserTier(objectField(object, "paidTier", "paid_tier")),
	}, nil
}

func parseGeminiCliUserTier(object map[string]json.RawMessage) *GeminiCliUserTier {
	if object == nil {
		return nil
	}
	tier := &GeminiCliUserTier{
		ID:          stringField(object, "id"),
		Name:        stringField(object, "name"),
		Description: stringField(object, "description"),
	}
	for _, raw := range arrayField(object, "availableCredits", "available_credits") {
		creditObject := rawObject(raw)
		if creditObject == nil {
			continue
		}
		credit := GeminiCliCredits{
			CreditType:       stringField(creditObject, "creditType", "credit_type"),
			rawPresenceKnown: true,
		}
		if value := floatPtrField(creditObject, "creditAmount", "credit_amount"); value != nil {
			credit.CreditAmount = *value
			credit.creditAmountPresent = true
		}
		tier.AvailableCredits = append(tier.AvailableCredits, credit)
	}
	return tier
}

func parseClaudeUsagePayload(response *apicall.Response) (*ClaudeUsagePayload, error) {
	object, err := parseResponseObject(response)
	if err != nil {
		return nil, err
	}
	return &ClaudeUsagePayload{
		FiveHour:          parseClaudeUsageWindow(objectField(object, "five_hour", "fiveHour")),
		SevenDay:          parseClaudeUsageWindow(objectField(object, "seven_day", "sevenDay")),
		SevenDayOAuthApps: parseClaudeUsageWindow(objectField(object, "seven_day_oauth_apps", "sevenDayOauthApps")),
		SevenDayOpus:      parseClaudeUsageWindow(objectField(object, "seven_day_opus", "sevenDayOpus")),
		SevenDaySonnet:    parseClaudeUsageWindow(objectField(object, "seven_day_sonnet", "sevenDaySonnet")),
		SevenDayCowork:    parseClaudeUsageWindow(objectField(object, "seven_day_cowork", "sevenDayCowork")),
		IguanaNecktie:     parseClaudeUsageWindow(objectField(object, "iguana_necktie", "iguanaNecktie")),
		ExtraUsage:        parseClaudeExtraUsage(objectField(object, "extra_usage", "extraUsage")),
	}, nil
}

func parseClaudeUsageWindow(object map[string]json.RawMessage) *ClaudeUsageWindow {
	if object == nil {
		return nil
	}
	window := &ClaudeUsageWindow{
		rawPresenceKnown: true,
		ResetsAt:         stringField(object, "resets_at", "resetsAt"),
	}
	if value := floatPtrField(object, "utilization"); value != nil {
		window.Utilization = *value
		window.utilizationPresent = true
	}
	window.resetsAtRaw = rawScalarField(object, "resets_at", "resetsAt")
	return window
}

func parseClaudeExtraUsage(object map[string]json.RawMessage) *ClaudeExtraUsage {
	if object == nil {
		return nil
	}
	extra := &ClaudeExtraUsage{
		IsEnabled:        boolField(object, "is_enabled", "isEnabled"),
		Utilization:      floatPtrField(object, "utilization"),
		rawPresenceKnown: true,
	}
	if value := floatPtrField(object, "monthly_limit", "monthlyLimit"); value != nil {
		extra.MonthlyLimit = *value
		extra.monthlyLimitPresent = true
	}
	if value := floatPtrField(object, "used_credits", "usedCredits"); value != nil {
		extra.UsedCredits = *value
		extra.usedCreditsPresent = true
	}
	return extra
}

func parseClaudeProfilePayload(response *apicall.Response) (*ClaudeProfileResponse, error) {
	object, err := parseResponseObject(response)
	if err != nil {
		return nil, err
	}
	return &ClaudeProfileResponse{
		Account:      parseClaudeProfileAccount(objectField(object, "account")),
		Organization: parseClaudeProfileOrganization(objectField(object, "organization")),
	}, nil
}

func parseClaudeProfileAccount(object map[string]json.RawMessage) *ClaudeProfileAccount {
	if object == nil {
		return nil
	}
	return &ClaudeProfileAccount{
		UUID:         stringField(object, "uuid"),
		FullName:     stringField(object, "full_name", "fullName"),
		DisplayName:  stringField(object, "display_name", "displayName"),
		Email:        stringField(object, "email"),
		HasClaudeMax: boolPtrField(object, "has_claude_max", "hasClaudeMax"),
		HasClaudePro: boolPtrField(object, "has_claude_pro", "hasClaudePro"),
	}
}

func parseClaudeProfileOrganization(object map[string]json.RawMessage) *ClaudeProfileOrganization {
	if object == nil {
		return nil
	}
	return &ClaudeProfileOrganization{
		UUID:                 stringField(object, "uuid"),
		Name:                 stringField(object, "name"),
		OrganizationType:     stringField(object, "organization_type", "organizationType"),
		BillingType:          stringField(object, "billing_type", "billingType"),
		RateLimitTier:        stringField(object, "rate_limit_tier", "rateLimitTier"),
		HasExtraUsageEnabled: boolField(object, "has_extra_usage_enabled", "hasExtraUsageEnabled"),
		SubscriptionStatus:   stringField(object, "subscription_status", "subscriptionStatus"),
	}
}

func parseKimiUsagePayload(response *apicall.Response) (*KimiUsagePayload, error) {
	object, err := parseResponseObject(response)
	if err != nil {
		return nil, err
	}
	payload := &KimiUsagePayload{Usage: parseKimiUsageDetail(objectField(object, "usage"))}
	for _, raw := range arrayField(object, "limits") {
		limitObject := rawObject(raw)
		if limitObject == nil {
			continue
		}
		detail := parseKimiUsageDetail(objectField(limitObject, "detail"))
		limit := KimiLimitItem{
			Name:             stringField(limitObject, "name"),
			Title:            stringField(limitObject, "title"),
			Scope:            stringField(limitObject, "scope"),
			Detail:           detail,
			Window:           parseKimiLimitWindow(objectField(limitObject, "window")),
			Duration:         intField(limitObject, "duration"),
			TimeUnit:         stringField(limitObject, "timeUnit", "time_unit"),
			ResetAt:          stringField(limitObject, "resetAt", "reset_at", "resetTime", "reset_time"),
			TTL:              floatField(limitObject, "ttl"),
			rawPresenceKnown: true,
			resetAtRaw:       rawScalarField(limitObject, "resetAt", "reset_at", "resetTime", "reset_time"),
		}
		if value := floatPtrField(limitObject, "used"); value != nil {
			limit.Used = *value
			limit.usedPresent = true
		}
		if value := floatPtrField(limitObject, "limit"); value != nil {
			limit.Limit = *value
			limit.limitPresent = true
		}
		if value := floatPtrField(limitObject, "remaining"); value != nil {
			limit.Remaining = *value
			limit.remainingPresent = true
		}
		if value := floatPtrField(limitObject, "resetIn", "reset_in"); value != nil {
			limit.ResetIn = *value
			limit.resetInPresent = true
		}
		if detail != nil {
			if limit.ResetAt == "" {
				limit.ResetAt = detail.ResetAt
				limit.resetAtRaw = detail.resetAtRaw
			}
			if !limit.resetInPresent && detail.resetInPresent {
				limit.ResetIn = detail.ResetIn
				limit.resetInPresent = true
			}
			if limit.TTL == 0 {
				limit.TTL = detail.TTL
			}
		}
		payload.Limits = append(payload.Limits, limit)
	}
	return payload, nil
}

func parseXAIBillingPayload(response *apicall.Response) (*XAIBillingPayload, error) {
	object, err := parseResponseObject(response)
	if err != nil {
		return nil, err
	}
	if _, ok := object["config"]; !ok {
		if nested := objectField(object, "body"); nested != nil {
			object = nested
		}
	}
	configObject := objectField(object, "config")
	payload := &XAIBillingPayload{Config: parseXAIBillingConfig(configObject)}
	return payload, nil
}

func parseXAIBillingConfig(object map[string]json.RawMessage) *XAIBillingConfig {
	if object == nil {
		return nil
	}
	config := &XAIBillingConfig{
		CurrentPeriod:       parseXAIBillingPeriod(objectField(object, "currentPeriod", "current_period")),
		CreditUsagePercent:  xaiFloatPtrField(object, "creditUsagePercent", "credit_usage_percent"),
		MonthlyLimit:        parseXAIMoneyField(object, "monthlyLimit", "monthly_limit"),
		Used:                parseXAIMoneyField(object, "used"),
		OnDemandCap:         parseXAIMoneyField(object, "onDemandCap", "on_demand_cap"),
		OnDemandUsed:        parseXAIMoneyField(object, "onDemandUsed", "on_demand_used"),
		BillingPeriodStart:  stringField(object, "billingPeriodStart", "billing_period_start"),
		BillingPeriodEnd:    stringField(object, "billingPeriodEnd", "billing_period_end"),
		billingPeriodEndRaw: rawScalarField(object, "billingPeriodEnd", "billing_period_end"),
	}
	for _, raw := range arrayField(object, "productUsage", "product_usage") {
		productObject := rawObject(raw)
		if productObject == nil {
			continue
		}
		config.ProductUsage = append(config.ProductUsage, XAIBillingProductUsage{
			Product:      stringField(productObject, "product"),
			UsagePercent: xaiFloatPtrField(productObject, "usagePercent", "usage_percent"),
		})
	}
	for _, raw := range arrayField(object, "history") {
		historyObject := rawObject(raw)
		if historyObject == nil {
			continue
		}
		config.History = append(config.History, XAIBillingHistoryItem{
			BillingCycle: parseXAIBillingCycle(objectField(historyObject, "billingCycle", "billing_cycle")),
			IncludedUsed: parseXAIMoneyField(historyObject, "includedUsed", "included_used"),
			OnDemandUsed: parseXAIMoneyField(historyObject, "onDemandUsed", "on_demand_used"),
			TotalUsed:    parseXAIMoneyField(historyObject, "totalUsed", "total_used"),
		})
	}
	return config
}

func parseXAIBillingCycle(object map[string]json.RawMessage) XAIBillingCycle {
	if object == nil {
		return XAIBillingCycle{}
	}
	return XAIBillingCycle{
		Year:  intField(object, "year"),
		Month: intField(object, "month"),
	}
}

func parseXAIBillingPeriod(object map[string]json.RawMessage) *XAIBillingPeriod {
	if object == nil {
		return nil
	}
	period := &XAIBillingPeriod{
		Type:  stringField(object, "type"),
		Start: stringField(object, "start"),
		End:   stringField(object, "end"),
	}
	period.endRaw = rawScalarField(object, "end")
	return period
}

func parseXAIMoneyField(object map[string]json.RawMessage, keys ...string) XAIMoneyValue {
	for _, key := range keys {
		raw, ok := object[key]
		if !ok || rawJSONNull(raw) {
			continue
		}
		if valueObject := rawObject(raw); valueObject != nil {
			return XAIMoneyValue{Val: xaiFloatPtrField(valueObject, "val")}
		}
		return XAIMoneyValue{Val: xaiFloatPtrField(map[string]json.RawMessage{"value": raw}, "value")}
	}
	return XAIMoneyValue{}
}

func xaiFloatPtrField(object map[string]json.RawMessage, keys ...string) *float64 {
	value := floatPtrField(object, keys...)
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) {
		return nil
	}
	return value
}

func parseKimiUsageDetail(object map[string]json.RawMessage) *KimiUsageDetail {
	if object == nil {
		return nil
	}
	used, hasUsed := floatValue(object, "used")
	limit, hasLimit := floatValue(object, "limit")
	remaining, hasRemaining := floatValue(object, "remaining")
	usedDerived := false
	// Kimi 省略 used 时按 provider 的 limit - remaining 关系补齐展示值，同时保留 raw presence 供 observation 只存事实。
	if !hasUsed && hasLimit && hasRemaining {
		used = limit - remaining
		usedDerived = true
	}
	detail := &KimiUsageDetail{
		Used:             used,
		Limit:            limit,
		Remaining:        remaining,
		Name:             stringField(object, "name"),
		Title:            stringField(object, "title"),
		ResetAt:          stringField(object, "resetAt", "reset_at", "resetTime", "reset_time"),
		ResetIn:          floatField(object, "resetIn", "reset_in"),
		TTL:              floatField(object, "ttl"),
		rawPresenceKnown: true,
		usedPresent:      hasUsed,
		usedDerived:      usedDerived,
		limitPresent:     hasLimit,
		remainingPresent: hasRemaining,
		resetAtRaw:       rawScalarField(object, "resetAt", "reset_at", "resetTime", "reset_time"),
	}
	if value := floatPtrField(object, "resetIn", "reset_in"); value != nil {
		detail.resetInPresent = true
	}
	return detail
}

func parseKimiLimitWindow(object map[string]json.RawMessage) *KimiLimitWindow {
	if object == nil {
		return nil
	}
	return &KimiLimitWindow{
		Duration: intField(object, "duration"),
		TimeUnit: stringField(object, "timeUnit", "time_unit"),
	}
}

func parseCodexResetCreditResponse(response *apicall.Response) (ProviderResetOutput, error) {
	object, err := parseResponseObject(response)
	if err != nil {
		return ProviderResetOutput{}, err
	}
	// consume 成功必须返回 code=reset 和 windows_reset，避免把未知成功体当成已重置。
	code := stringField(object, "code")
	if code != "reset" {
		return ProviderResetOutput{}, fmt.Errorf("unexpected reset credit response code: %s", code)
	}
	windowsReset := intPtrField(object, "windows_reset", "windowsReset")
	if windowsReset == nil {
		return ProviderResetOutput{}, fmt.Errorf("invalid reset credit response")
	}
	return ProviderResetOutput{
		Code:         code,
		WindowsReset: int(*windowsReset),
	}, nil
}

func parseCodexResetCreditsResponse(response *apicall.Response) (ProviderResetCreditsOutput, error) {
	object, err := parseResponseObject(response)
	if err != nil {
		return ProviderResetCreditsOutput{}, err
	}
	if _, hasCredits := object["credits"]; !hasCredits {
		if _, hasCount := object["available_count"]; !hasCount {
			if _, hasCamelCount := object["availableCount"]; !hasCamelCount {
				return ProviderResetCreditsOutput{}, fmt.Errorf("invalid reset credits response")
			}
		}
	}
	credits := make([]CodexRateLimitResetCredit, 0)
	for _, raw := range arrayField(object, "credits") {
		creditObject := rawObject(raw)
		if creditObject == nil || stringField(creditObject, "reset_type", "resetType") != "codex_rate_limits" || stringField(creditObject, "status") != "available" {
			continue
		}
		expiresAt := stringField(creditObject, "expires_at", "expiresAt")
		if expiresAt == "" {
			continue
		}
		credits = append(credits, CodexRateLimitResetCredit{
			ID:        stringField(creditObject, "id"),
			Status:    "available",
			GrantedAt: stringField(creditObject, "granted_at", "grantedAt"),
			ExpiresAt: expiresAt,
		})
	}
	var availableCount *int
	if count := intPtrField(object, "available_count", "availableCount"); count != nil && *count >= 0 {
		parsedCount := int(*count)
		availableCount = &parsedCount
	}
	return ProviderResetCreditsOutput{AvailableCount: availableCount, Credits: credits}, nil
}

func parseResponseObject(response *apicall.Response) (map[string]json.RawMessage, error) {
	if response == nil {
		return nil, fmt.Errorf("missing quota response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, targetHTTPError(response)
	}
	if object := rawObject(response.Body); object != nil {
		return object, nil
	}
	trimmed := strings.TrimSpace(response.BodyText)
	if trimmed == "" {
		return nil, fmt.Errorf("empty quota response body")
	}
	if object := rawObject([]byte(trimmed)); object != nil {
		return object, nil
	}
	return nil, fmt.Errorf("parse quota response body")
}

func rawObject(data []byte) map[string]json.RawMessage {
	if len(data) == 0 {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err == nil && object != nil {
		return object
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		return rawObject([]byte(strings.TrimSpace(text)))
	}
	return nil
}

func objectField(object map[string]json.RawMessage, keys ...string) map[string]json.RawMessage {
	for _, key := range keys {
		if raw, ok := object[key]; ok {
			return rawObject(raw)
		}
	}
	return nil
}

func arrayField(object map[string]json.RawMessage, keys ...string) []json.RawMessage {
	for _, key := range keys {
		if raw, ok := object[key]; ok {
			var values []json.RawMessage
			if err := json.Unmarshal(raw, &values); err == nil {
				return values
			}
		}
	}
	return nil
}

func stringField(object map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		if raw, ok := object[key]; ok {
			var text string
			if err := json.Unmarshal(raw, &text); err == nil {
				return strings.TrimSpace(text)
			}
			var number json.Number
			if err := json.Unmarshal(raw, &number); err == nil {
				return number.String()
			}
		}
	}
	return ""
}

func rawScalarField(object map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		raw, ok := object[key]
		if !ok || rawJSONNull(raw) {
			continue
		}
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			return text
		}
		return strings.TrimSpace(string(raw))
	}
	return ""
}

func floatField(object map[string]json.RawMessage, keys ...string) float64 {
	value, _ := floatValue(object, keys...)
	return value
}

func floatPtrField(object map[string]json.RawMessage, keys ...string) *float64 {
	value, ok := floatValue(object, keys...)
	if !ok {
		return nil
	}
	return &value
}

func quotaFractionPtrField(object map[string]json.RawMessage, keys ...string) *float64 {
	for _, key := range keys {
		raw, ok := object[key]
		if !ok || rawJSONNull(raw) {
			continue
		}
		var number float64
		if err := json.Unmarshal(raw, &number); err == nil {
			if !math.IsNaN(number) && !math.IsInf(number, 0) {
				return &number
			}
			continue
		}
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			continue
		}
		text = strings.TrimSpace(text)
		scale := 1.0
		if strings.HasSuffix(text, "%") {
			text = strings.TrimSpace(strings.TrimSuffix(text, "%"))
			scale = 0.01
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil {
			continue
		}
		parsed *= scale
		if !math.IsNaN(parsed) && !math.IsInf(parsed, 0) {
			return &parsed
		}
	}
	return nil
}

func floatValue(object map[string]json.RawMessage, keys ...string) (float64, bool) {
	for _, key := range keys {
		if raw, ok := object[key]; ok {
			if rawJSONNull(raw) {
				continue
			}
			var number float64
			if err := json.Unmarshal(raw, &number); err == nil {
				return number, true
			}
			var text string
			if err := json.Unmarshal(raw, &text); err == nil {
				parsed, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
				if err == nil {
					return parsed, true
				}
			}
		}
	}
	return 0, false
}

func rawJSONNull(raw json.RawMessage) bool {
	return len(raw) == 4 && raw[0] == 'n' && raw[1] == 'u' && raw[2] == 'l' && raw[3] == 'l'
}

func intField(object map[string]json.RawMessage, keys ...string) int64 {
	value, ok := floatValue(object, keys...)
	if !ok {
		return 0
	}
	return int64(value)
}

func intPtrField(object map[string]json.RawMessage, keys ...string) *int64 {
	value, ok := floatValue(object, keys...)
	if !ok {
		return nil
	}
	parsed := int64(value)
	return &parsed
}

func boolField(object map[string]json.RawMessage, keys ...string) bool {
	value := boolPtrField(object, keys...)
	return value != nil && *value
}

func boolPtrField(object map[string]json.RawMessage, keys ...string) *bool {
	for _, key := range keys {
		if raw, ok := object[key]; ok {
			if rawJSONNull(raw) {
				continue
			}
			var value bool
			if err := json.Unmarshal(raw, &value); err == nil {
				return &value
			}
			var text string
			if err := json.Unmarshal(raw, &text); err == nil {
				switch strings.ToLower(strings.TrimSpace(text)) {
				case "true", "1", "yes", "y", "on":
					parsed := true
					return &parsed
				case "false", "0", "no", "n", "off":
					parsed := false
					return &parsed
				}
			}
		}
	}
	return nil
}
