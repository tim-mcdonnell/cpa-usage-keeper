package pricing

import (
	"testing"

	"cpa-usage-keeper/internal/entities"
)

func TestCanonicalSnapshotContentHashIgnoresModelOrder(t *testing.T) {
	t.Parallel()

	multiplier := 1.0
	modelA := ModelConfig{Pricing: entities.ModelPriceSetting{
		Model:           "model-a",
		PricingStyle:    entities.ModelPricingStyleOpenAI,
		PriceMultiplier: &multiplier,
	}}
	modelB := ModelConfig{Pricing: entities.ModelPriceSetting{
		Model:           "model-b",
		PricingStyle:    entities.ModelPricingStyleOpenAI,
		PriceMultiplier: &multiplier,
	}}

	first := canonicalSnapshotContentHash([]ModelConfig{modelB, modelA})
	second := canonicalSnapshotContentHash([]ModelConfig{modelA, modelB})
	if first != second {
		t.Fatalf("semantically identical model orderings have different hashes: %q != %q", first, second)
	}
}
