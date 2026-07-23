package pricing

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"math"
	"sort"
)

const snapshotHashDomain = "cpa-usage-keeper/pricing-snapshot/v1"

func canonicalSnapshotContentHash(configs []ModelConfig) string {
	digest := sha256.New()
	writeHashString(digest, snapshotHashDomain)

	orderedConfigs := append([]ModelConfig(nil), configs...)
	sort.Slice(orderedConfigs, func(i, j int) bool {
		return orderedConfigs[i].Pricing.Model < orderedConfigs[j].Pricing.Model
	})
	writeHashUint64(digest, uint64(len(orderedConfigs)))

	for _, config := range orderedConfigs {
		pricing := config.Pricing
		writeHashString(digest, pricing.Model)
		writeHashString(digest, pricing.PricingStyle)
		writeHashFloat64(digest, pricing.PromptPricePer1M)
		writeHashFloat64(digest, pricing.CompletionPricePer1M)
		writeHashFloat64(digest, pricing.CacheReadPricePer1M)
		writeHashFloat64(digest, pricing.CacheWritePricePer1M)

		modelMultiplier := 1.0
		if pricing.PriceMultiplier != nil {
			modelMultiplier = *pricing.PriceMultiplier
		}
		writeHashFloat64(digest, modelMultiplier)

		rules := make([]RuleConfig, 0, len(config.Rules))
		for _, rule := range config.Rules {
			// Match resolver semantics: multiplier-one rules cannot change cost.
			if rule.Multiplier != 1 {
				rules = append(rules, rule)
			}
		}
		sort.Slice(rules, func(i, j int) bool {
			if rules[i].Key != rules[j].Key {
				return rules[i].Key < rules[j].Key
			}
			return rules[i].Value < rules[j].Value
		})
		writeHashUint64(digest, uint64(len(rules)))
		for _, rule := range rules {
			writeHashString(digest, rule.Key)
			writeHashString(digest, rule.Value)
			writeHashFloat64(digest, rule.Multiplier)
		}
	}

	return hex.EncodeToString(digest.Sum(nil))
}

func writeHashString(digest hash.Hash, value string) {
	writeHashUint64(digest, uint64(len(value)))
	_, _ = digest.Write([]byte(value))
}

func writeHashFloat64(digest hash.Hash, value float64) {
	if value == 0 {
		// Normalize negative zero because it has the same pricing semantics as positive zero.
		value = 0
	}
	writeHashUint64(digest, math.Float64bits(value))
}

func writeHashUint64(digest hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = digest.Write(encoded[:])
}
