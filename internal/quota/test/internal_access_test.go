package test

import (
	"context"
	"net/http"
	"reflect"
	"time"
	"unsafe"

	"cpa-usage-keeper/internal/quota"
)

const (
	quotaWindowFiveHourSeconds     int64 = 5 * 60 * 60
	quotaWindowSevenDaySeconds     int64 = 7 * 24 * 60 * 60
	quotaWindowThirtyDaySeconds    int64 = 30 * 24 * 60 * 60
	quotaWindowAverageMonthSeconds int64 = 365 * 24 * 60 * 60 / 12
)

//go:linkname parseCodexHeaderQuota cpa-usage-keeper/internal/quota.parseCodexHeaderQuota
func parseCodexHeaderQuota(headers http.Header) (quota.ProviderOutput, bool)

//go:linkname quotaRowUsageWindow cpa-usage-keeper/internal/quota.quotaRowUsageWindow
func quotaRowUsageWindow(row quota.QuotaRow, now time.Time) (time.Time, time.Time, bool)

//go:linkname attachWindowUsageStats cpa-usage-keeper/internal/quota.(*Service).attachWindowUsageStats
func attachWindowUsageStats(service *quota.Service, ctx context.Context, authIndex string, response quota.CheckResponse, now time.Time) quota.CheckResponse

//go:linkname applyUsageHeaderSnapshot cpa-usage-keeper/internal/quota.(*Service).applyUsageHeaderSnapshot
func applyUsageHeaderSnapshot(service *quota.Service, ctx context.Context, snapshot quota.UsageHeaderSnapshot) bool

//go:linkname applyUsageHeaderSnapshots cpa-usage-keeper/internal/quota.(*Service).applyUsageHeaderSnapshots
func applyUsageHeaderSnapshots(service *quota.Service, ctx context.Context, snapshots []quota.UsageHeaderSnapshot)

//go:linkname mergePendingUsageHeaderSnapshots cpa-usage-keeper/internal/quota.mergePendingUsageHeaderSnapshots
func mergePendingUsageHeaderSnapshots(pending map[string]quota.UsageHeaderSnapshot, snapshots []quota.UsageHeaderSnapshot)

//go:linkname cleanupExpiredRefreshTasks cpa-usage-keeper/internal/quota.(*Service).cleanupExpiredRefreshTasks
func cleanupExpiredRefreshTasks(service *quota.Service, now time.Time)

//go:linkname nextAutoRefreshDelay cpa-usage-keeper/internal/quota.(*Service).nextAutoRefreshDelay
func nextAutoRefreshDelay(service *quota.Service, settings quota.AutoRefreshSettings, now time.Time) time.Duration

//go:linkname sleepAutoRefreshDelay cpa-usage-keeper/internal/quota.(*Service).sleepAutoRefreshDelay
func sleepAutoRefreshDelay(service *quota.Service, ctx context.Context, delay time.Duration) int

//go:linkname resetInspectionCompletedAt cpa-usage-keeper/internal/quota.(*Service).resetInspectionCompletedAt
func resetInspectionCompletedAt(service *quota.Service)

//go:linkname sortInspectionResults cpa-usage-keeper/internal/quota.sortInspectionResults
func sortInspectionResults(results []quota.InspectionResult)

func quotaServiceField(service *quota.Service, name string) reflect.Value {
	value := reflect.ValueOf(service).Elem().FieldByName(name)
	return reflect.NewAt(value.Type(), unsafe.Pointer(value.UnsafeAddr())).Elem()
}

func setRefreshCooldown(service *quota.Service, cooldown func(time.Duration)) {
	quotaServiceField(service, "refreshCooldown").Set(reflect.ValueOf(cooldown))
}

func refreshWorkerTokens(service *quota.Service) chan struct{} {
	return quotaServiceField(service, "refreshWorkerTokens").Interface().(chan struct{})
}

func refreshWorkerTokenCap(service *quota.Service) int {
	return cap(refreshWorkerTokens(service))
}

func occupyRefreshWorkerToken(service *quota.Service) func() {
	tokens := refreshWorkerTokens(service)
	tokens <- struct{}{}
	return func() { <-tokens }
}

func refreshTasks(service *quota.Service) map[string]*quota.RefreshTaskRecord {
	return quotaServiceField(service, "refreshTasks").Interface().(map[string]*quota.RefreshTaskRecord)
}

func setRefreshTasks(service *quota.Service, tasks map[string]*quota.RefreshTaskRecord) {
	quotaServiceField(service, "refreshTasks").Set(reflect.ValueOf(tasks))
}

func setRefreshTask(service *quota.Service, authIndex string, task *quota.RefreshTaskRecord) {
	refreshTasks(service)[authIndex] = task
}

func refreshTaskCount(service *quota.Service) int {
	return len(refreshTasks(service))
}

func refreshTaskRecord(service *quota.Service, authIndex string) *quota.RefreshTaskRecord {
	return refreshTasks(service)[authIndex]
}

func setLastAutoRefreshRoundAt(service *quota.Service, at time.Time) {
	quotaServiceField(service, "lastAutoRefreshRoundAt").Set(reflect.ValueOf(at))
}

func setLastAutoRefreshAttemptAt(service *quota.Service, at time.Time) {
	quotaServiceField(service, "lastAutoRefreshAttemptAt").Set(reflect.ValueOf(at))
}

func setAutoRefreshRunning(service *quota.Service, running bool) {
	quotaServiceField(service, "autoRefreshRunning").SetBool(running)
}

func setAutoRefreshNow(service *quota.Service, now func() time.Time) {
	quotaServiceField(service, "autoRefreshNow").Set(reflect.ValueOf(now))
}

func setAutoRefreshDelay(service *quota.Service, delay func(context.Context, time.Duration) bool) {
	quotaServiceField(service, "autoRefreshDelay").Set(reflect.ValueOf(delay))
}

func autoRefreshSettingsChanged(service *quota.Service) chan struct{} {
	return quotaServiceField(service, "autoRefreshSettingsChanged").Interface().(chan struct{})
}

func lastAutoRefreshRoundAt(service *quota.Service) time.Time {
	return quotaServiceField(service, "lastAutoRefreshRoundAt").Interface().(time.Time)
}

func lastAutoRefreshAttemptAt(service *quota.Service) time.Time {
	return quotaServiceField(service, "lastAutoRefreshAttemptAt").Interface().(time.Time)
}

func usageHeaderFlushInterval(service *quota.Service) time.Duration {
	return quotaServiceField(service, "usageHeaderFlushInterval").Interface().(time.Duration)
}

func setUsageHeaderTimerFactory(service *quota.Service, factory func(time.Duration) (<-chan time.Time, func())) {
	quotaServiceField(service, "usageHeaderNewTimer").Set(reflect.ValueOf(factory))
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
