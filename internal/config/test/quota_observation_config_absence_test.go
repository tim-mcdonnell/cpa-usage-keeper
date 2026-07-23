package test

import (
	"reflect"
	"strings"
	"testing"

	"cpa-usage-keeper/internal/config"
)

func TestQuotaObservationRecordingHasNoConfigurationSurface(t *testing.T) {
	configType := reflect.TypeOf(config.Config{})
	for index := 0; index < configType.NumField(); index++ {
		field := configType.Field(index)
		name := strings.ToLower(field.Name + " " + field.Tag.Get("env"))
		if strings.Contains(name, "observation") || strings.Contains(name, "quota_record") {
			t.Fatalf("quota observation recording must always be on, found config field %s", field.Name)
		}
	}
}
