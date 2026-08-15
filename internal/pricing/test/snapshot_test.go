package test

import (
	"math"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
)

func TestCompileSnapshotNormalizesRulesAndExcludesIdentityRulesFromActiveFields(t *testing.T) {
	t.Parallel()

	snapshot, err := pricing.CompileSnapshot([]pricing.ModelConfig{{
		Pricing: testPricing(" model-b ", 2),
		Rules: []pricing.RuleConfig{
			{Key: " SERVICE_TIER ", Value: " priority ", Multiplier: 2},
			{Key: "reasoning_effort", Value: " xhigh ", Multiplier: 1},
		},
	}, {
		Pricing: testPricing("model-a", 1),
	}})
	if err != nil {
		t.Fatalf("CompileSnapshot returned error: %v", err)
	}

	active := snapshot.ActiveFields()
	if !active.Has(pricing.RuleFieldServiceTier) {
		t.Fatal("expected service_tier to be active")
	}
	if active.Has(pricing.RuleFieldReasoningEffort) {
		t.Fatal("expected multiplier-1 reasoning_effort rule to stay inactive")
	}
	configs := snapshot.ModelConfigs()
	if len(configs) != 2 || configs[0].Pricing.Model != "model-a" || configs[1].Pricing.Model != "model-b" {
		t.Fatalf("expected stable model-sorted configs, got %+v", configs)
	}
	rule := configs[1].Rules[0]
	if rule.Key != "service_tier" || rule.Value != "priority" || rule.Multiplier != 2 {
		t.Fatalf("expected normalized rule, got %+v", rule)
	}
	if len(configs[1].Rules) != 2 || configs[1].Rules[1].Multiplier != 1 {
		t.Fatalf("expected multiplier-1 rule to remain visible, got %+v", configs[1].Rules)
	}
}

func TestCompileSnapshotDefensivelyCopiesInputsAndOutputs(t *testing.T) {
	t.Parallel()

	multiplier := 2.0
	configs := []pricing.ModelConfig{{
		Pricing: entities.ModelPriceSetting{
			Model:            "model-a",
			PromptPricePer1M: 3,
			PriceMultiplier:  &multiplier,
		},
		Rules: []pricing.RuleConfig{{Key: "service_tier", Value: "priority", Multiplier: 2}},
	}}
	snapshot, err := pricing.CompileSnapshot(configs)
	if err != nil {
		t.Fatalf("CompileSnapshot returned error: %v", err)
	}

	configs[0].Pricing.Model = "mutated"
	multiplier = 100
	configs[0].Rules[0].Value = "mutated"
	first := snapshot.ModelConfigs()
	first[0].Pricing.Model = "changed-output"
	*first[0].Pricing.PriceMultiplier = 200
	first[0].Rules[0].Value = "changed-output"

	second := snapshot.ModelConfigs()
	if second[0].Pricing.Model != "model-a" || *second[0].Pricing.PriceMultiplier != 2 || second[0].Rules[0].Value != "priority" {
		t.Fatalf("snapshot exposed mutable state: %+v", second)
	}
}

func TestSnapshotContentHashIsStableAcrossRecompilesAndPersistenceMetadata(t *testing.T) {
	t.Parallel()

	multiplier := 1.5
	ruleTime := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	first := []pricing.ModelConfig{{
		Pricing: entities.ModelPriceSetting{
			ID:                   100,
			Model:                "model-b",
			PricingStyle:         entities.ModelPricingStyleClaude,
			PromptPricePer1M:     3,
			CompletionPricePer1M: 15,
			CacheReadPricePer1M:  0.3,
			CacheWritePricePer1M: 3.75,
			PriceMultiplier:      &multiplier,
			CreatedAt:            ruleTime,
			UpdatedAt:            ruleTime,
		},
		Rules: []pricing.RuleConfig{{Key: "service_tier", Value: "priority", Multiplier: 2}},
	}}
	second := []pricing.ModelConfig{{
		Pricing: entities.ModelPriceSetting{
			ID:                   999,
			Model:                "model-b",
			PricingStyle:         entities.ModelPricingStyleClaude,
			PromptPricePer1M:     3,
			CompletionPricePer1M: 15,
			CacheReadPricePer1M:  0.3,
			CacheWritePricePer1M: 3.75,
			PriceMultiplier:      &multiplier,
			CreatedAt:            ruleTime.Add(time.Hour),
			UpdatedAt:            ruleTime.Add(2 * time.Hour),
		},
		Rules: []pricing.RuleConfig{{Key: "service_tier", Value: "priority", Multiplier: 2}},
	}}

	firstSnapshot := mustCompileSnapshot(t, first)
	secondSnapshot := mustCompileSnapshot(t, second)
	if firstSnapshot.ContentHash() != secondSnapshot.ContentHash() {
		t.Fatalf("semantically identical snapshots have different hashes: %q != %q", firstSnapshot.ContentHash(), secondSnapshot.ContentHash())
	}
	if len(firstSnapshot.ContentHash()) != 64 {
		t.Fatalf("expected lowercase SHA-256 hash, got %q", firstSnapshot.ContentHash())
	}
}

func TestSnapshotContentHashIgnoresRuleOrder(t *testing.T) {
	t.Parallel()

	modelA := pricing.ModelConfig{
		Pricing: testPricing("model-a", 1),
		Rules: []pricing.RuleConfig{
			{Key: "service_tier", Value: "priority", Multiplier: 2},
			{Key: "reasoning_effort", Value: "xhigh", Multiplier: 3},
		},
	}

	first := mustCompileSnapshot(t, []pricing.ModelConfig{modelA})
	modelA.Rules[0], modelA.Rules[1] = modelA.Rules[1], modelA.Rules[0]
	second := mustCompileSnapshot(t, []pricing.ModelConfig{modelA})
	if first.ContentHash() != second.ContentHash() {
		t.Fatalf("semantically identical rule orderings have different hashes: %q != %q", first.ContentHash(), second.ContentHash())
	}
}

func TestSnapshotContentHashIgnoresMultiplierOneRules(t *testing.T) {
	t.Parallel()

	base := pricing.ModelConfig{Pricing: testPricing("model-a", 1)}
	withNoOpRule := cloneTestModelConfig(base)
	withNoOpRule.Rules = []pricing.RuleConfig{{
		Key:        "service_tier",
		Value:      "priority",
		Multiplier: 1,
	}}
	withoutRule := mustCompileSnapshot(t, []pricing.ModelConfig{base})
	withRule := mustCompileSnapshot(t, []pricing.ModelConfig{withNoOpRule})
	if withoutRule.ContentHash() != withRule.ContentHash() {
		t.Fatalf("multiplier-one rule changed content hash: %q != %q", withoutRule.ContentHash(), withRule.ContentHash())
	}
}

func TestSnapshotContentHashTreatsNegativeZeroAsPositiveZero(t *testing.T) {
	t.Parallel()

	negativeZero := math.Copysign(0, -1)
	positiveMultiplier := 2.0
	negative := pricing.ModelConfig{
		Pricing: entities.ModelPriceSetting{
			Model:                "model-a",
			PricingStyle:         entities.ModelPricingStyleOpenAI,
			PromptPricePer1M:     negativeZero,
			CompletionPricePer1M: negativeZero,
			CacheReadPricePer1M:  negativeZero,
			CacheWritePricePer1M: negativeZero,
			PriceMultiplier:      &positiveMultiplier,
		},
		Rules: []pricing.RuleConfig{{Key: "service_tier", Value: "priority", Multiplier: negativeZero}},
	}
	positive := cloneTestModelConfig(negative)
	positive.Pricing.PromptPricePer1M = 0
	positive.Pricing.CompletionPricePer1M = 0
	positive.Pricing.CacheReadPricePer1M = 0
	positive.Pricing.CacheWritePricePer1M = 0
	positive.Rules[0].Multiplier = 0

	negativeSnapshot := mustCompileSnapshot(t, []pricing.ModelConfig{negative})
	positiveSnapshot := mustCompileSnapshot(t, []pricing.ModelConfig{positive})
	if negativeSnapshot.ContentHash() != positiveSnapshot.ContentHash() {
		t.Fatalf("negative-zero prices or rule multiplier changed content hash: %q != %q", negativeSnapshot.ContentHash(), positiveSnapshot.ContentHash())
	}

	negativeMultiplier := negativeZero
	positiveMultiplier = 0
	negative.Pricing = testPricing("model-a", 1)
	negative.Pricing.PriceMultiplier = &negativeMultiplier
	negative.Rules = nil
	positive = cloneTestModelConfig(negative)
	positive.Pricing.PriceMultiplier = &positiveMultiplier
	negativeSnapshot = mustCompileSnapshot(t, []pricing.ModelConfig{negative})
	positiveSnapshot = mustCompileSnapshot(t, []pricing.ModelConfig{positive})
	if negativeSnapshot.ContentHash() != positiveSnapshot.ContentHash() {
		t.Fatalf("negative-zero model multiplier changed content hash: %q != %q", negativeSnapshot.ContentHash(), positiveSnapshot.ContentHash())
	}
}

func TestSnapshotContentHashChangesForEveryCostAffectingValue(t *testing.T) {
	t.Parallel()

	base := pricing.ModelConfig{
		Pricing: testPricing("model-a", 1.5),
		Rules: []pricing.RuleConfig{{
			Key:        "service_tier",
			Value:      "priority",
			Multiplier: 2,
		}},
	}
	baseHash := mustCompileSnapshot(t, []pricing.ModelConfig{base}).ContentHash()

	tests := []struct {
		name   string
		mutate func(*pricing.ModelConfig)
	}{
		{"model", func(config *pricing.ModelConfig) { config.Pricing.Model = "model-b" }},
		{"pricing style", func(config *pricing.ModelConfig) { config.Pricing.PricingStyle = entities.ModelPricingStyleClaude }},
		{"prompt price", func(config *pricing.ModelConfig) { config.Pricing.PromptPricePer1M = 1.1 }},
		{"completion price", func(config *pricing.ModelConfig) { config.Pricing.CompletionPricePer1M = 2.1 }},
		{"cache read price", func(config *pricing.ModelConfig) { config.Pricing.CacheReadPricePer1M = 0.2 }},
		{"cache write price", func(config *pricing.ModelConfig) { config.Pricing.CacheWritePricePer1M = 1.5 }},
		{"model multiplier", func(config *pricing.ModelConfig) {
			multiplier := 1.6
			config.Pricing.PriceMultiplier = &multiplier
		}},
		{"rule key", func(config *pricing.ModelConfig) { config.Rules[0].Key = "reasoning_effort" }},
		{"rule value", func(config *pricing.ModelConfig) { config.Rules[0].Value = "batch" }},
		{"rule multiplier", func(config *pricing.ModelConfig) { config.Rules[0].Multiplier = 2.1 }},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			changed := cloneTestModelConfig(base)
			testCase.mutate(&changed)
			changedHash := mustCompileSnapshot(t, []pricing.ModelConfig{changed}).ContentHash()
			if changedHash == baseHash {
				t.Fatalf("changing %s did not change content hash %q", testCase.name, baseHash)
			}
		})
	}
}

func TestCompileSnapshotRejectsInvalidModelsPricesAndRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		configs []pricing.ModelConfig
	}{
		{"blank model", []pricing.ModelConfig{{Pricing: testPricing(" ", 1)}}},
		{"duplicate model", []pricing.ModelConfig{{Pricing: testPricing("same", 1)}, {Pricing: testPricing(" same ", 1)}}},
		{"negative price", []pricing.ModelConfig{{Pricing: entities.ModelPriceSetting{Model: "model", PromptPricePer1M: -1}}}},
		{"nan price", []pricing.ModelConfig{{Pricing: entities.ModelPriceSetting{Model: "model", PromptPricePer1M: math.NaN()}}}},
		{"negative model multiplier", []pricing.ModelConfig{{Pricing: testPricing("model", -1)}}},
		{"infinite model multiplier", []pricing.ModelConfig{{Pricing: testPricing("model", math.Inf(1))}}},
		{"unknown key", []pricing.ModelConfig{{Pricing: testPricing("model", 1), Rules: []pricing.RuleConfig{{Key: "provider", Value: "openai", Multiplier: 2}}}}},
		{"blank value", []pricing.ModelConfig{{Pricing: testPricing("model", 1), Rules: []pricing.RuleConfig{{Key: "service_tier", Value: " ", Multiplier: 2}}}}},
		{"negative rule multiplier", []pricing.ModelConfig{{Pricing: testPricing("model", 1), Rules: []pricing.RuleConfig{{Key: "service_tier", Value: "priority", Multiplier: -1}}}}},
		{"nan rule multiplier", []pricing.ModelConfig{{Pricing: testPricing("model", 1), Rules: []pricing.RuleConfig{{Key: "service_tier", Value: "priority", Multiplier: math.NaN()}}}}},
		{"duplicate normalized rule", []pricing.ModelConfig{{Pricing: testPricing("model", 1), Rules: []pricing.RuleConfig{{Key: "service_tier", Value: "priority", Multiplier: 2}, {Key: " SERVICE_TIER ", Value: " priority ", Multiplier: 3}}}}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := pricing.CompileSnapshot(tt.configs); err == nil {
				t.Fatalf("expected CompileSnapshot to reject %s", tt.name)
			}
		})
	}
}

func TestCompileSnapshotRejectsFiniteInputsWhoseWorstCaseCostOverflows(t *testing.T) {
	t.Parallel()

	for _, config := range []pricing.ModelConfig{
		{Pricing: entities.ModelPriceSetting{Model: "huge-price", PromptPricePer1M: math.MaxFloat64}},
		{
			Pricing: testPricing("huge-rule-product", 1),
			Rules: []pricing.RuleConfig{
				{Key: "service_tier", Value: "priority", Multiplier: math.MaxFloat64 / 2},
				{Key: "reasoning_effort", Value: "xhigh", Multiplier: 2},
			},
		},
	} {
		if _, err := pricing.CompileSnapshot([]pricing.ModelConfig{config}); err == nil {
			t.Fatalf("expected overflow configuration to fail: %+v", config)
		}
	}
}

func TestCompileSnapshotRejectsUnscaledSegmentSumOverflowHiddenBySmallMultiplier(t *testing.T) {
	t.Parallel()

	maxTokensPerMillion := float64(math.MaxInt64) / 1_000_000
	segmentPrice := (math.MaxFloat64 * 0.6) / maxTokensPerMillion
	multiplier := 0.25
	config := pricing.ModelConfig{Pricing: entities.ModelPriceSetting{
		Model:                "hidden-overflow",
		PromptPricePer1M:     segmentPrice,
		CompletionPricePer1M: segmentPrice,
		PriceMultiplier:      &multiplier,
	}}

	if _, err := pricing.CompileSnapshot([]pricing.ModelConfig{config}); err == nil {
		t.Fatal("expected the unscaled input and output sum overflow to be rejected")
	}
}

func TestCompileSnapshotAllowsZeroModelMultiplierWithHugeFiniteRules(t *testing.T) {
	t.Parallel()

	config := pricing.ModelConfig{
		Pricing: testPricing("free-model", 0),
		Rules: []pricing.RuleConfig{
			{Key: "service_tier", Value: "priority", Multiplier: math.MaxFloat64},
			{Key: "reasoning_effort", Value: "xhigh", Multiplier: math.MaxFloat64},
		},
	}
	if _, err := pricing.CompileSnapshot([]pricing.ModelConfig{config}); err != nil {
		t.Fatalf("zero model multiplier must keep final cost finite: %v", err)
	}
}

func testPricing(model string, multiplier float64) entities.ModelPriceSetting {
	return entities.ModelPriceSetting{
		Model:                model,
		PricingStyle:         entities.ModelPricingStyleOpenAI,
		PromptPricePer1M:     1,
		CompletionPricePer1M: 2,
		CacheReadPricePer1M:  0.1,
		CacheWritePricePer1M: 1.25,
		PriceMultiplier:      &multiplier,
	}
}

func mustCompileSnapshot(t *testing.T, configs []pricing.ModelConfig) *pricing.Snapshot {
	t.Helper()
	snapshot, err := pricing.CompileSnapshot(configs)
	if err != nil {
		t.Fatalf("CompileSnapshot returned error: %v", err)
	}
	return snapshot
}

func cloneTestModelConfig(config pricing.ModelConfig) pricing.ModelConfig {
	cloned := config
	cloned.Pricing = config.Pricing
	if config.Pricing.PriceMultiplier != nil {
		multiplier := *config.Pricing.PriceMultiplier
		cloned.Pricing.PriceMultiplier = &multiplier
	}
	cloned.Rules = append([]pricing.RuleConfig(nil), config.Rules...)
	return cloned
}
