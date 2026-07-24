package quota

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestCapacityObservationReadLimitsCoverNineEpochsAndCalendarBoundaries(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name          string
		windowSeconds int64
		want          int
	}{
		{
			name:          "five hour",
			windowSeconds: int64(5 * time.Hour / time.Second),
			want:          1200,
		},
		{
			name:          "seven day",
			windowSeconds: int64(7 * 24 * time.Hour / time.Second),
			want:          25600,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := capacityObservationReadLimit(testCase.windowSeconds); got != testCase.want {
				t.Fatalf("capacity observation read limit = %d, want %d", got, testCase.want)
			}
		})
	}
}

func TestCapacityBatchRequiresAtLeastOneCredential(t *testing.T) {
	t.Parallel()
	service := &Service{}
	for _, request := range []CapacityRequest{
		{},
		{AuthIndexes: []string{"", "  "}},
	} {
		if _, err := service.GetCapacity(context.Background(), request); !errors.Is(err, ErrValidation) {
			t.Fatalf("GetCapacity error = %v, want validation", err)
		}
	}
}

func TestCapacityBatchCredentialLimitUsesNormalizedDistinctCount(t *testing.T) {
	t.Parallel()
	service := &Service{}
	exactlyLimit := make([]string, CapacityMaxAuthIndexes)
	for index := range exactlyLimit {
		exactlyLimit[index] = fmt.Sprintf("auth-%03d", index)
	}
	overLimit := append(append([]string(nil), exactlyLimit...), "auth-over-limit")

	if _, err := service.GetCapacity(context.Background(), CapacityRequest{
		AuthIndexes: overLimit,
	}); !errors.Is(err, ErrCapacityAuthIndexLimit) {
		t.Fatalf("101 distinct auth indexes error = %v, want capacity limit", err)
	}
	for name, authIndexes := range map[string][]string{
		"exactly 100": exactlyLimit,
		"duplicates normalize to 100": append(
			append([]string(nil), exactlyLimit...),
			" auth-000 ",
			"auth-099",
		),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.GetCapacity(context.Background(), CapacityRequest{
				AuthIndexes: authIndexes,
			})
			if errors.Is(err, ErrValidation) {
				t.Fatalf("accepted boundary returned validation error: %v", err)
			}
		})
	}
}
