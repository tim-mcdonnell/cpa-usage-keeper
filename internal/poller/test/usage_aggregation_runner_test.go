package poller_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cpa-usage-keeper/internal/config"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/poller"
	"cpa-usage-keeper/internal/quota"
	"cpa-usage-keeper/internal/repository"
	repositorydto "cpa-usage-keeper/internal/repository/dto"

	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"gorm.io/gorm"
)

func TestUsageAggregationRunnerDefersToProcessableInboxAndRotatesIndependentCheckpoints(t *testing.T) {
	// 准备：使用真实单连接 SQLite，写入 event、identity 和仍为 pending 的 inbox。
	db := openUsageAggregationRunnerDatabase(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	events := []entities.UsageEvent{{EventKey: "runner-event", APIGroupKey: "provider-a", Model: "model-a", AuthType: "oauth", AuthIndex: "auth-a", Timestamp: now.Add(-time.Minute), InputTokens: 10, CacheReadTokens: 2, TotalTokens: 12}}
	if _, _, err := repository.InsertUsageEvents(db, events); err != nil {
		t.Fatalf("insert runner event: %v", err)
	}
	identity := entities.UsageIdentity{Name: "auth-a", AuthType: entities.UsageIdentityAuthTypeAuthFile, Identity: "auth-a", Type: "codex"}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatalf("insert runner identity: %v", err)
	}
	inboxRows, err := repository.InsertRedisUsageInboxMessages(db, []repositorydto.RedisInboxInsert{{Source: "redis_pull:usage", RawMessage: `{"request_id":"pending"}`, PoppedAt: now}})
	if err != nil {
		t.Fatalf("insert pending inbox: %v", err)
	}

	// 执行：runner 在 pending inbox 存在时尝试第一轮调度。
	runner := poller.NewUsageAggregationRunner(db, nil)
	result, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce with pending inbox: %v", err)
	}
	// 断言：必须让路且两个聚合 checkpoint 都不能被创建。
	if !result.DeferredForInbox || result.Processed {
		t.Fatalf("expected aggregation to defer for inbox, got %+v", result)
	}
	assertUsageAggregationCheckpointMissing(t, db, "overview")
	assertUsageAggregationCheckpointMissing(t, db, "activity")

	// process runner 提交 processed 后，下一次 RunOnce 才允许 Overview 获得 writer。
	if err := repository.MarkRedisUsageInboxProcessed(db, inboxRows[0].ID, "runner-event", now); err != nil {
		t.Fatalf("mark inbox processed: %v", err)
	}
	result, err = runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("overview RunOnce: %v", err)
	}
	if !result.Processed || result.Kind != poller.UsageAggregationKindOverview {
		t.Fatalf("expected overview batch, got %+v", result)
	}
	assertUsageAggregationCheckpoint(t, db, "overview", 1)
	assertUsageAggregationCheckpointMissing(t, db, "activity")

	// 一个事务结束后轮转到 Activity，并再次独立推进自己的 checkpoint。
	result, err = runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("activity RunOnce: %v", err)
	}
	if !result.Processed || result.Kind != poller.UsageAggregationKindActivity {
		t.Fatalf("expected activity batch, got %+v", result)
	}
	assertUsageAggregationCheckpoint(t, db, "overview", 1)
	assertUsageAggregationCheckpoint(t, db, "activity", 1)

	// 第三个事务公平轮转到 Identity，并复用每行既有 cursor 独立累计旧表。
	result, err = runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("identity RunOnce: %v", err)
	}
	if !result.Processed || result.Kind != poller.UsageAggregationKindIdentity {
		t.Fatalf("expected identity batch, got %+v", result)
	}
	var storedIdentity entities.UsageIdentity
	if err := db.First(&storedIdentity, identity.ID).Error; err != nil {
		t.Fatalf("load aggregated identity: %v", err)
	}
	if storedIdentity.TotalRequests != 1 || storedIdentity.CacheReadTokens != 2 || storedIdentity.LastAggregatedUsageEventID != 1 {
		t.Fatalf("unexpected identity aggregation result: %+v", storedIdentity)
	}
}

func TestUsageAggregationRunnerRetriesFailedActivityWithoutChangingOverview(t *testing.T) {
	// 准备：一条事件让 Overview 和 Activity 都产生待处理 batch，并准备 Activity 故障 trigger。
	db := openUsageAggregationRunnerDatabase(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	events := []entities.UsageEvent{{EventKey: "runner-failure", APIGroupKey: "provider-a", Model: "model-a", Timestamp: now.Add(-time.Minute), InputTokens: 10, CacheReadTokens: 2, TotalTokens: 12}}
	if _, _, err := repository.InsertUsageEvents(db, events); err != nil {
		t.Fatalf("insert runner failure event: %v", err)
	}
	runner := poller.NewUsageAggregationRunner(db, nil)

	// 执行：先提交 Overview，再强制 Activity 失败，最后移除 trigger 重试。
	result, err := runner.RunOnce(context.Background())
	if err != nil || result.Kind != poller.UsageAggregationKindOverview {
		t.Fatalf("overview RunOnce result=%+v err=%v", result, err)
	}
	// 断言：Overview checkpoint 已先独立提交。
	assertUsageAggregationCheckpoint(t, db, "overview", 1)

	// trigger 强制 Activity INSERT 失败，模拟新聚合自身错误。
	if err := db.Exec(`CREATE TRIGGER fail_runner_activity
		BEFORE INSERT ON usage_activity_stats
		BEGIN
			SELECT RAISE(ABORT, 'forced runner activity failure');
		END`).Error; err != nil {
		t.Fatalf("create activity failure trigger: %v", err)
	}
	result, err = runner.RunOnce(context.Background())
	if err == nil {
		t.Fatalf("expected activity failure, got %+v", result)
	}
	assertUsageAggregationCheckpoint(t, db, "overview", 1)
	assertUsageAggregationCheckpointMissing(t, db, "activity")

	// 移除故障后必须重试同一个 Activity kind，而不是错误地跳到下一事务。
	if err := db.Exec("DROP TRIGGER fail_runner_activity").Error; err != nil {
		t.Fatalf("drop activity failure trigger: %v", err)
	}
	result, err = runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("retry activity RunOnce: %v", err)
	}
	if result.Kind != poller.UsageAggregationKindActivity || !result.Processed {
		t.Fatalf("expected retried activity batch, got %+v", result)
	}
	assertUsageAggregationCheckpoint(t, db, "overview", 1)
	assertUsageAggregationCheckpoint(t, db, "activity", 1)
}

func TestUsageAggregationRunnerGatesHeaderSnapshotsOnlyOnOverviewCheckpoint(t *testing.T) {
	// 准备：记录 appender 收到的 snapshot，并写入对应 usage event。
	db := openUsageAggregationRunnerDatabase(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	events := []entities.UsageEvent{{EventKey: "runner-header", APIGroupKey: "provider-a", Model: "model-a", Timestamp: now.Add(-time.Minute), AuthIndex: "auth-a", InputTokens: 10, TotalTokens: 10}}
	if _, _, err := repository.InsertUsageEvents(db, events); err != nil {
		t.Fatalf("insert header event: %v", err)
	}
	var storedEvents []entities.UsageEvent
	if err := db.Order("id asc").Find(&storedEvents).Error; err != nil {
		t.Fatalf("load stored header events: %v", err)
	}
	appender := &recordingUsageAggregationHeaderAppender{accept: true}
	runner := poller.NewUsageAggregationRunner(db, appender)
	snapshot := quota.UsageHeaderSnapshot{
		AuthType: "oauth", AuthIndex: "auth-a", Provider: "openai", ObservedAt: now,
		Headers: http.Header{"X-Codex-Primary-Used-Percent": []string{"12"}},
	}

	// 执行：notifier 先写内存，随后只运行第一轮 Overview batch。
	runner.NotifyUsageEventsCommitted(storedEvents, []quota.UsageHeaderSnapshot{snapshot})
	// 断言：Overview checkpoint 推进前 appender 不能收到 snapshot。
	if appender.callCount != 0 {
		t.Fatalf("expected header snapshot gated before overview, got %d calls", appender.callCount)
	}

	// 第一轮只提交 Overview；提交后 gate 满足并投递 snapshot。
	result, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("overview header RunOnce: %v", err)
	}
	if result.Kind != poller.UsageAggregationKindOverview {
		t.Fatalf("expected overview batch, got %+v", result)
	}
	if appender.callCount != 1 || len(appender.snapshots) != 1 || appender.snapshots[0].AuthIndex != "auth-a" {
		t.Fatalf("unexpected appended snapshots: calls=%d snapshots=%+v", appender.callCount, appender.snapshots)
	}
	assertUsageAggregationCheckpointMissing(t, db, "activity")
}

func TestUsageAggregationRunnerKeepsOnlyLatestPendingHeaderTriggerPerAuthIndex(t *testing.T) {
	db := openUsageAggregationRunnerDatabase(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	events := []entities.UsageEvent{
		{EventKey: "runner-header-old", APIGroupKey: "provider-a", Model: "model-a", Timestamp: now.Add(-2 * time.Minute), AuthIndex: "auth-a", InputTokens: 10, TotalTokens: 10},
		{EventKey: "runner-header-new", APIGroupKey: "provider-a", Model: "model-a", Timestamp: now.Add(-time.Minute), AuthIndex: "auth-a", InputTokens: 20, TotalTokens: 20},
	}
	if _, _, err := repository.InsertUsageEvents(db, events); err != nil {
		t.Fatalf("insert coalesced header events: %v", err)
	}
	var storedEvents []entities.UsageEvent
	if err := db.Order("id asc").Find(&storedEvents).Error; err != nil {
		t.Fatalf("load coalesced header events: %v", err)
	}
	appender := &recordingUsageAggregationHeaderAppender{accept: true}
	runner := poller.NewUsageAggregationRunner(db, appender)
	runner.NotifyUsageEventsCommitted(storedEvents[:1], []quota.UsageHeaderSnapshot{{
		AuthType:           "oauth",
		AuthIndex:          "auth-a",
		Provider:           "codex",
		ObservedAt:         now.Add(-time.Minute),
		TriggeringEventKey: "runner-header-old",
		Headers:            http.Header{"X-Codex-Primary-Used-Percent": []string{"12"}},
	}})
	runner.NotifyUsageEventsCommitted(storedEvents[1:], []quota.UsageHeaderSnapshot{{
		AuthType:           "oauth",
		AuthIndex:          "auth-a",
		Provider:           "codex",
		ObservedAt:         now,
		TriggeringEventKey: "runner-header-new",
		Headers:            http.Header{"X-Codex-Primary-Used-Percent": []string{"13"}},
	}})

	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("overview coalesced header RunOnce: %v", err)
	}
	if appender.callCount != 1 ||
		len(appender.snapshots) != 1 ||
		appender.snapshots[0].TriggeringEventKey != "runner-header-new" ||
		appender.snapshots[0].Headers.Get("X-Codex-Primary-Used-Percent") != "13" {
		t.Fatalf("unexpected surviving runner snapshot: calls=%d snapshots=%+v", appender.callCount, appender.snapshots)
	}
}

func TestUsageAggregationRunnerStopsReportingIdentityWorkAfterFinalCleanPage(t *testing.T) {
	// 准备：创建超过一页且没有待聚合事件的 identities，迫使 runner 扫描两页才能确认追平。
	db := openUsageAggregationRunnerDatabase(t)
	identities := make([]entities.UsageIdentity, 0, repository.UsageIdentityAggregationBatchSize+1)
	for index := 0; index < repository.UsageIdentityAggregationBatchSize+1; index++ {
		identity := entities.UsageIdentity{Name: fmt.Sprintf("clean-identity-%02d", index), AuthType: entities.UsageIdentityAuthTypeAuthFile, Identity: fmt.Sprintf("clean-identity-%02d", index), Type: "codex"}
		identities = append(identities, identity)
	}
	if err := db.Create(&identities).Error; err != nil {
		t.Fatalf("seed clean identities: %v", err)
	}
	runner := poller.NewUsageAggregationRunner(db, nil)

	// 执行：先轮转空 Overview/Activity，再扫描仍有下一页的第一批 Identity。
	for transaction := 0; transaction < 2; transaction++ {
		if _, err := runner.RunOnce(context.Background()); err != nil {
			t.Fatalf("prelude transaction %d: %v", transaction+1, err)
		}
	}
	firstPage, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("first identity page: %v", err)
	}
	// 断言：即使第一页没有 delta，只要仍有下一页，就必须保持当前 wake 继续扫描。
	if firstPage.Kind != poller.UsageAggregationKindIdentity || !firstPage.Processed {
		t.Fatalf("expected non-final clean identity page to keep scanning, got %+v", firstPage)
	}
	// 执行：再次轮转空 Overview/Activity，随后读取已经到达末尾的 Identity 尾页。
	for transaction := 0; transaction < 2; transaction++ {
		if _, err := runner.RunOnce(context.Background()); err != nil {
			t.Fatalf("second prelude transaction %d: %v", transaction+1, err)
		}
	}
	finalPage, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("final identity page: %v", err)
	}

	// 断言：最终页没有任何 delta 时必须报告空闲，让后台循环结束本轮而不是从头无限重扫。
	if finalPage.Kind != poller.UsageAggregationKindIdentity || finalPage.Processed {
		t.Fatalf("expected clean final identity page to stop reporting work, got %+v", finalPage)
	}
}

func TestUsageAggregationRunnerRescansIdentityHeadAfterNotificationDuringPagedPass(t *testing.T) {
	// 准备：创建超过一页的 identities，并先让 Runner 完成第一页空扫描。
	db := openUsageAggregationRunnerDatabase(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	identities := make([]entities.UsageIdentity, 0, repository.UsageIdentityAggregationBatchSize+1)
	for index := 0; index < repository.UsageIdentityAggregationBatchSize+1; index++ {
		identity := fmt.Sprintf("generation-identity-%02d", index)
		identities = append(identities, entities.UsageIdentity{Name: identity, AuthType: entities.UsageIdentityAuthTypeAuthFile, Identity: identity, Type: "codex"})
	}
	if err := db.Create(&identities).Error; err != nil {
		t.Fatalf("seed generation identities: %v", err)
	}
	runner := poller.NewUsageAggregationRunner(db, nil)
	for transaction := 0; transaction < 3; transaction++ {
		if _, err := runner.RunOnce(context.Background()); err != nil {
			t.Fatalf("initial paged transaction %d: %v", transaction+1, err)
		}
	}

	// 执行：第一页之后提交属于最小 ID identity 的事件并发送 usage 通知，再完成 Overview、Activity 和旧尾页。
	events := []entities.UsageEvent{{EventKey: "identity-generation-event", APIGroupKey: "provider-a", Model: "model-a", AuthType: "oauth", AuthIndex: identities[0].Identity, Timestamp: now, TotalTokens: 1}}
	if _, _, err := repository.InsertUsageEvents(db, events); err != nil {
		t.Fatalf("insert generation event: %v", err)
	}
	var storedEvents []entities.UsageEvent
	if err := db.Order("id asc").Find(&storedEvents).Error; err != nil {
		t.Fatalf("load generation event: %v", err)
	}
	runner.NotifyUsageEventsCommitted(storedEvents, nil)
	for transaction := 0; transaction < 2; transaction++ {
		if _, err := runner.RunOnce(context.Background()); err != nil {
			t.Fatalf("generation rollup transaction %d: %v", transaction+1, err)
		}
	}
	tail, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("generation identity tail: %v", err)
	}

	// 断言：旧尾页虽然没有 delta，也必须报告需要从 ID 头部重扫，防止后台在下一次 Identity 前休眠。
	if tail.Kind != poller.UsageAggregationKindIdentity || !tail.Processed {
		t.Fatalf("expected stale identity pass to request rescan, got %+v", tail)
	}
	for transaction := 0; transaction < 3; transaction++ {
		if _, err := runner.RunOnce(context.Background()); err != nil {
			t.Fatalf("identity rescan transaction %d: %v", transaction+1, err)
		}
	}
	var firstIdentity entities.UsageIdentity
	if err := db.First(&firstIdentity, identities[0].ID).Error; err != nil {
		t.Fatalf("load rescanned identity: %v", err)
	}
	if firstIdentity.TotalRequests != 1 || firstIdentity.LastAggregatedUsageEventID != storedEvents[0].ID {
		t.Fatalf("expected first identity to catch up after rescan, got %+v", firstIdentity)
	}
}

func TestUsageAggregationRunnerKeepsHeaderSnapshotsWhenAppenderRejects(t *testing.T) {
	// 准备：写入一条带自增 ID 的事件，并让 appender 第一次模拟队列已满。
	db := openUsageAggregationRunnerDatabase(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	events := []entities.UsageEvent{{EventKey: "runner-header-retry", APIGroupKey: "provider-a", Model: "model-a", Timestamp: now.Add(-time.Minute), AuthIndex: "auth-a", InputTokens: 10, TotalTokens: 10}}
	if _, _, err := repository.InsertUsageEvents(db, events); err != nil {
		t.Fatalf("insert retry header event: %v", err)
	}
	var storedEvents []entities.UsageEvent
	if err := db.Order("id asc").Find(&storedEvents).Error; err != nil {
		t.Fatalf("load retry header event: %v", err)
	}
	appender := &recordingUsageAggregationHeaderAppender{accept: false}
	runner := poller.NewUsageAggregationRunner(db, appender)
	snapshot := quota.UsageHeaderSnapshot{AuthType: "oauth", AuthIndex: "auth-a", Provider: "openai", ObservedAt: now, Headers: http.Header{"X-Codex-Primary-Used-Percent": []string{"12"}}}
	runner.NotifyUsageEventsCommitted(storedEvents, []quota.UsageHeaderSnapshot{snapshot})

	// 执行：Overview 达到 gate 时第一次投递被拒绝，再完整轮转后允许第二次投递。
	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("first overview RunOnce: %v", err)
	}
	appender.accept = true
	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("activity RunOnce: %v", err)
	}
	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("identity RunOnce: %v", err)
	}
	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("second overview RunOnce: %v", err)
	}

	// 断言：同一个 snapshot 在拒绝后没有丢失，且成功接收后只记录一次。
	if appender.callCount != 2 {
		t.Fatalf("expected two append attempts, got %d", appender.callCount)
	}
	if len(appender.snapshots) != 1 || appender.snapshots[0].AuthIndex != "auth-a" {
		t.Fatalf("unexpected accepted snapshots: %+v", appender.snapshots)
	}
}

func TestUsageAggregationRunnerRunRetriesRejectedHeaderWithoutAnotherWake(t *testing.T) {
	// 准备：appender 连续拒绝前两次投递，且 runner 启动后不再发送任何 notifier wake。
	db := openUsageAggregationRunnerDatabase(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	events := []entities.UsageEvent{{EventKey: "runner-header-background-retry", APIGroupKey: "provider-a", Model: "model-a", Timestamp: now.Add(-time.Minute), AuthIndex: "auth-a", InputTokens: 10, TotalTokens: 10}}
	if _, _, err := repository.InsertUsageEvents(db, events); err != nil {
		t.Fatalf("insert background retry event: %v", err)
	}
	var storedEvents []entities.UsageEvent
	if err := db.Order("id asc").Find(&storedEvents).Error; err != nil {
		t.Fatalf("load background retry event: %v", err)
	}
	appender := &acceptThirdUsageAggregationHeaderAppender{}
	runner := poller.NewUsageAggregationRunner(db, appender)
	snapshot := quota.UsageHeaderSnapshot{AuthType: "oauth", AuthIndex: "auth-a", Provider: "openai", ObservedAt: now, Headers: http.Header{"X-Codex-Primary-Used-Percent": []string{"12"}}}
	runner.NotifyUsageEventsCommitted(storedEvents, []quota.UsageHeaderSnapshot{snapshot})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)

	// 执行：只启动后台循环，等待它在没有新 wake 的情况下完成第三次有界重试。
	go func() {
		done <- runner.Run(ctx)
	}()
	waitForUsageAggregationRunnerCondition(t, func() bool {
		return appender.accepted.Load() == 1
	})
	cancel()

	// 断言：snapshot 最终只成功接收一次，且空闲 Runner 能响应 context cancellation。
	if appender.calls.Load() < 3 || appender.accepted.Load() != 1 {
		t.Fatalf("unexpected background retry counts: calls=%d accepted=%d", appender.calls.Load(), appender.accepted.Load())
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runner Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not stop after background header retry")
	}
}

func TestUsageAggregationRunnerShutdownCompletesStartedTransaction(t *testing.T) {
	// 准备：写入一条事件，并在 Overview hourly INSERT 前暂停已经开始的短事务。
	db := openUsageAggregationRunnerDatabase(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	events := []entities.UsageEvent{{EventKey: "runner-shutdown-transaction", APIGroupKey: "provider-a", Model: "model-a", Timestamp: now, TotalTokens: 1}}
	if _, _, err := repository.InsertUsageEvents(db, events); err != nil {
		t.Fatalf("insert shutdown transaction event: %v", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var paused atomic.Bool
	callbackName := "test:pause_usage_aggregation_hourly_create"
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		// 只暂停 Runner 首次创建 hourly row 的事务，避免影响测试准备数据。
		if tx.Statement.Table == "usage_overview_hourly_stats" && paused.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}); err != nil {
		t.Fatalf("register shutdown transaction callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })
	runner := poller.NewUsageAggregationRunner(db, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	// 执行：Runner 进入事务后取消 App context，再允许当前 INSERT 继续。
	go func() { done <- runner.Run(ctx) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("runner did not enter overview transaction")
	}
	cancel()
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runner returned shutdown error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not stop after completing transaction")
	}

	// 断言：已经开始的 Overview rows 与 checkpoint 必须完整提交，而不是因 shutdown cancel 回滚。
	assertUsageAggregationCheckpoint(t, db, "overview", 1)
}

func TestUsageAggregationRunnerShutdownFlushesReadyHeaderSnapshots(t *testing.T) {
	// 准备：先追平 Overview checkpoint，再放入一份已经满足 gate 的内存 header snapshot。
	db := openUsageAggregationRunnerDatabase(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	events := []entities.UsageEvent{{EventKey: "runner-shutdown-header", APIGroupKey: "provider-a", Model: "model-a", AuthIndex: "auth-shutdown", Timestamp: now, TotalTokens: 1}}
	if _, _, err := repository.InsertUsageEvents(db, events); err != nil {
		t.Fatalf("insert shutdown header event: %v", err)
	}
	if err := repository.AggregateUsageOverviewStats(context.Background(), db, now); err != nil {
		t.Fatalf("aggregate shutdown header overview: %v", err)
	}
	var storedEvents []entities.UsageEvent
	if err := db.Order("id asc").Find(&storedEvents).Error; err != nil {
		t.Fatalf("load shutdown header event: %v", err)
	}
	appender := &recordingUsageAggregationHeaderAppender{accept: true}
	runner := poller.NewUsageAggregationRunner(db, appender)
	runner.NotifyUsageEventsCommitted(storedEvents, []quota.UsageHeaderSnapshot{{AuthIndex: "auth-shutdown", ObservedAt: now, Headers: http.Header{"X-Test": []string{"ready"}}}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// 执行：使用已经取消的 App context 进入 Runner 关闭路径。
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("runner shutdown returned error: %v", err)
	}

	// 断言：关闭前必须非阻塞投递已经越过 Overview gate 的 snapshot。
	if appender.callCount != 1 || len(appender.snapshots) != 1 || appender.snapshots[0].AuthIndex != "auth-shutdown" {
		t.Fatalf("expected ready shutdown snapshot to flush once, got calls=%d snapshots=%+v", appender.callCount, appender.snapshots)
	}
}

func TestUsageAggregationRunnerShutdownWithoutPendingHeadersSkipsDatabaseWait(t *testing.T) {
	// 准备：占用唯一数据库连接，并配置非空 header appender 但不放入任何 pending snapshot。
	db := openUsageAggregationRunnerDatabase(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("load sql db: %v", err)
	}
	heldConnection, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("hold database connection: %v", err)
	}
	t.Cleanup(func() { _ = heldConnection.Close() })
	runner := poller.NewUsageAggregationRunner(db, &recordingUsageAggregationHeaderAppender{accept: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)

	// 执行：以已取消 context 进入只需要执行 shutdown defer 的 Runner。
	go func() { done <- runner.Run(ctx) }()

	// 断言：没有等待快照时 shutdown 不应为了读取 Overview cursor 等待数据库连接。
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("runner shutdown returned error: %v", runErr)
		}
	case <-time.After(time.Second):
		_ = heldConnection.Close()
		<-done
		t.Fatal("runner shutdown waited for database without pending header snapshots")
	}
}

func TestUsageAggregationRunnerShutdownBoundsPendingHeaderDatabaseWait(t *testing.T) {
	// 准备：占用唯一数据库连接，并放入一份需要查询 Overview cursor 的 pending snapshot。
	db := openUsageAggregationRunnerDatabase(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("load sql db: %v", err)
	}
	heldConnection, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("hold database connection: %v", err)
	}
	t.Cleanup(func() { _ = heldConnection.Close() })
	runner := poller.NewUsageAggregationRunner(db, &recordingUsageAggregationHeaderAppender{accept: true})
	runner.NotifyUsageEventsCommitted(
		[]entities.UsageEvent{{ID: 1}},
		[]quota.UsageHeaderSnapshot{{AuthIndex: "auth-pending", ObservedAt: time.Now(), Headers: http.Header{"X-Test": []string{"pending"}}}},
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)

	// 执行：关闭 Runner 时让 pending snapshot 的 cursor 查询等待唯一连接。
	go func() { done <- runner.Run(ctx) }()

	// 断言：best-effort shutdown flush 必须有上限，不能无限阻塞 App.Close。
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("runner shutdown returned error: %v", runErr)
		}
	case <-time.After(2 * time.Second):
		_ = heldConnection.Close()
		<-done
		t.Fatal("runner shutdown flush waited indefinitely for database connection")
	}
}

func TestUsageAggregationRunnerCancellationDoesNotLogBatchFailureWhileWaitingForConnection(t *testing.T) {
	// 准备：占用唯一数据库连接，让 startup wake 的 inbox 检查停在可取消的连接池等待。
	db := openUsageAggregationRunnerDatabase(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("load sql db: %v", err)
	}
	heldConnection, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("hold database connection: %v", err)
	}
	t.Cleanup(func() { _ = heldConnection.Close() })
	initialWaitCount := sqlDB.Stats().WaitCount
	hook := logrustest.NewGlobal()
	t.Cleanup(hook.Reset)
	runner := poller.NewUsageAggregationRunner(db, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	waitForUsageAggregationRunnerCondition(t, func() bool {
		return sqlDB.Stats().WaitCount > initialWaitCount
	})

	// 执行：在数据库等待期间发出正常 App shutdown cancellation。
	cancel()
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("runner cancellation returned error: %v", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not stop after canceling database wait")
	}

	// 断言：context cancellation 是正常关闭信号，不能记录成聚合批次错误。
	for _, entry := range hook.AllEntries() {
		if entry.Level == logrus.ErrorLevel && entry.Message == "usage aggregation batch failed" {
			t.Fatalf("normal cancellation emitted aggregation error log: %+v", entry)
		}
	}
}

func TestUsageAggregationRunnerShutdownStillLogsDetachedTransactionFailure(t *testing.T) {
	// 准备：用 SQLite trigger 制造真实 Overview 写入错误，并在写入开始前同时取消 App context。
	db := openUsageAggregationRunnerDatabase(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	if _, _, err := repository.InsertUsageEvents(db, []entities.UsageEvent{{EventKey: "runner-shutdown-error", APIGroupKey: "provider-a", Model: "model-a", Timestamp: now, TotalTokens: 1}}); err != nil {
		t.Fatalf("insert shutdown error event: %v", err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_shutdown_overview
		BEFORE INSERT ON usage_overview_hourly_stats
		BEGIN
			SELECT RAISE(ABORT, 'forced shutdown overview failure');
		END`).Error; err != nil {
		t.Fatalf("create shutdown failure trigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	callbackName := "test:cancel_before_shutdown_overview_failure"
	var canceled atomic.Bool
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		// 只在 detached Overview hourly 写入前取消 parent，事务仍会继续命中真实 trigger 错误。
		if tx.Statement.Table == "usage_overview_hourly_stats" && canceled.CompareAndSwap(false, true) {
			cancel()
		}
	}); err != nil {
		t.Fatalf("register shutdown failure callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })
	hook := logrustest.NewGlobal()
	t.Cleanup(hook.Reset)
	runner := poller.NewUsageAggregationRunner(db, nil)

	// 执行：startup wake 进入 Overview 事务，parent cancel 与独立 SQLite 错误在同一关闭窗口发生。
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("runner shutdown returned error: %v", err)
	}

	// 断言：只有 cancellation 自身可静默；detached 事务的真实失败仍必须留下错误日志。
	if !canceled.Load() {
		t.Fatal("expected callback to cancel App context before trigger failure")
	}
	for _, entry := range hook.AllEntries() {
		if entry.Level == logrus.ErrorLevel && entry.Message == "usage aggregation batch failed" {
			return
		}
	}
	t.Fatalf("detached transaction failure was hidden during shutdown: %+v", hook.AllEntries())
}

func TestUsageAggregationRunnerShutdownCancelsPostTransactionHeaderDatabaseWait(t *testing.T) {
	// 准备：写入待聚合事件和 header snapshot，并在 Overview checkpoint 更新时排队占用唯一连接。
	db := openUsageAggregationRunnerDatabase(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("load sql db: %v", err)
	}
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	if _, _, err := repository.InsertUsageEvents(db, []entities.UsageEvent{{EventKey: "runner-post-transaction-header", APIGroupKey: "provider-a", Model: "model-a", AuthIndex: "auth-header-wait", Timestamp: now, TotalTokens: 1}}); err != nil {
		t.Fatalf("insert post-transaction header event: %v", err)
	}
	var storedEvents []entities.UsageEvent
	if err := db.Order("id asc").Find(&storedEvents).Error; err != nil {
		t.Fatalf("load post-transaction header event: %v", err)
	}
	runner := poller.NewUsageAggregationRunner(db, &recordingUsageAggregationHeaderAppender{accept: true})
	runner.NotifyUsageEventsCommitted(storedEvents, []quota.UsageHeaderSnapshot{{AuthIndex: "auth-header-wait", ObservedAt: now, Headers: http.Header{"X-Test": []string{"pending"}}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	initialWaitCount := sqlDB.Stats().WaitCount
	startConnectionWaiter := make(chan struct{})
	heldConnection := make(chan *sql.Conn, 1)
	waiterError := make(chan error, 1)
	go func() {
		select {
		case <-startConnectionWaiter:
			// callback 已确认 Overview 事务持有连接，此时进入连接池等待队列。
		case <-ctx.Done():
			return
		}
		connection, connectionErr := sqlDB.Conn(ctx)
		if connectionErr != nil {
			waiterError <- connectionErr
			return
		}
		heldConnection <- connection
	}()
	callbackName := "test:queue_connection_before_post_transaction_header"
	var waiterStarted atomic.Bool
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		// Overview checkpoint 更新仍在事务内持有连接，此时先把外部 waiter 排到连接池队列头部。
		if tx.Statement.Table == "usage_overview_aggregation_checkpoints" && waiterStarted.CompareAndSwap(false, true) {
			close(startConnectionWaiter)
			deadline := time.Now().Add(time.Second)
			for sqlDB.Stats().WaitCount <= initialWaitCount && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
		}
	}); err != nil {
		t.Fatalf("register post-transaction header callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	var connection *sql.Conn
	select {
	case connection = <-heldConnection:
		t.Cleanup(func() { _ = connection.Close() })
	case connectionErr := <-waiterError:
		cancel()
		t.Fatalf("hold post-transaction database connection: %v", connectionErr)
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("connection waiter did not acquire the post-transaction slot")
	}
	waitForUsageAggregationRunnerCondition(t, func() bool {
		// 第一次 wait 是外部 holder，第二次 wait 证明 Header cursor 查询正在其后排队。
		return sqlDB.Stats().WaitCount >= initialWaitCount+2
	})

	// 执行：在事务已经提交、普通 Header cursor 查询等待连接时取消 App context。
	cancel()

	// 断言：普通 Header 查询必须响应取消；随后只允许 shutdown best-effort flush 再等待最多一秒。
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("runner shutdown returned error: %v", runErr)
		}
	case <-time.After(2 * time.Second):
		_ = connection.Close()
		<-done
		t.Fatal("post-transaction header query ignored App cancellation")
	}
}

func TestUsageAggregationRunnerBoundsPendingHeaderSnapshots(t *testing.T) {
	// 准备：预置满足 gate 的 Overview checkpoint，并构造超过 Runner 容量的不同 auth_index。
	db := openUsageAggregationRunnerDatabase(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	if err := db.Create(&entities.UsageOverviewAggregationCheckpoint{Name: "overview", LastAggregatedUsageEventID: 1, StatsUpdatedAt: &now}).Error; err != nil {
		t.Fatalf("seed bounded header checkpoint: %v", err)
	}
	appender := &recordingUsageAggregationHeaderAppender{accept: true}
	runner := poller.NewUsageAggregationRunner(db, appender)
	snapshots := make([]quota.UsageHeaderSnapshot, 0, 1001)
	for index := 0; index < 1001; index++ {
		snapshots = append(snapshots, quota.UsageHeaderSnapshot{AuthIndex: fmt.Sprintf("bounded-auth-%04d", index), ObservedAt: now, Headers: http.Header{"X-Test": []string{"bounded"}}})
	}
	runner.NotifyUsageEventsCommitted([]entities.UsageEvent{{ID: 1}}, snapshots)

	// 执行：Overview 空 batch 读取已提交 cursor，并投递当前内存中保留的 ready snapshots。
	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("flush bounded headers: %v", err)
	}

	// 断言：Runner 最多保留 1000 个 auth_index，禁止持续故障时内存无界增长。
	if len(appender.snapshots) != 1000 {
		t.Fatalf("expected 1000 bounded snapshots, got %d", len(appender.snapshots))
	}
}

func TestUsageAggregationRunnerRunWakesOnStartupAndKeepsOtherKindsMovingAfterFailure(t *testing.T) {
	// 准备：事件和 identity 在 runner 启动前已经存在，Activity trigger 持续失败。
	db := openUsageAggregationRunnerDatabase(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	identity := entities.UsageIdentity{Name: "runner-background", AuthType: entities.UsageIdentityAuthTypeAuthFile, Identity: "runner-background", Type: "codex"}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatalf("insert background identity: %v", err)
	}
	events := []entities.UsageEvent{{EventKey: "runner-background", APIGroupKey: "provider-a", Model: "model-a", AuthType: "oauth", AuthIndex: identity.Identity, Timestamp: now.Add(-time.Minute), InputTokens: 10, CachedTokens: 7, CacheReadTokens: 2, TotalTokens: 12}}
	if _, _, err := repository.InsertUsageEvents(db, events); err != nil {
		t.Fatalf("insert background event: %v", err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_background_activity
		BEFORE INSERT ON usage_activity_stats
		BEGIN
			SELECT RAISE(ABORT, 'forced background activity failure');
		END`).Error; err != nil {
		t.Fatalf("create background activity failure trigger: %v", err)
	}
	runner := poller.NewUsageAggregationRunner(db, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	// 执行：不发送 notifier，直接启动后台循环验证 startup wake 和失败隔离。
	go func() {
		done <- runner.Run(ctx)
	}()
	waitForUsageAggregationRunnerCondition(t, func() bool {
		var stored entities.UsageIdentity
		if err := db.First(&stored, identity.ID).Error; err != nil {
			return false
		}
		return stored.TotalRequests == 1 && stored.CachedTokens == 7 && stored.LastAggregatedUsageEventID == 1
	})
	cancel()

	// 断言：Activity 持续失败没有阻止旧 Overview 与 Identity 达到原有最终结果，且 runner 可正常关闭。
	assertUsageAggregationCheckpoint(t, db, "overview", 1)
	assertUsageAggregationCheckpointMissing(t, db, "activity")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runner Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not stop after context cancellation")
	}
}

func TestUsageAggregationRunnerRunKeepsActivityAndIdentityMovingAfterOverviewFailure(t *testing.T) {
	// 准备：写入同属三个聚合的事件和 identity，并让 Overview hourly INSERT 持续失败。
	db := openUsageAggregationRunnerDatabase(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	identity := entities.UsageIdentity{Name: "runner-overview-failure", AuthType: entities.UsageIdentityAuthTypeAuthFile, Identity: "runner-overview-failure", Type: "codex"}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatalf("insert overview failure identity: %v", err)
	}
	events := []entities.UsageEvent{{EventKey: "runner-overview-failure", APIGroupKey: "provider-a", Model: "model-a", AuthType: "oauth", AuthIndex: identity.Identity, Timestamp: now.Add(-time.Minute), InputTokens: 10, CachedTokens: 7, CacheReadTokens: 2, TotalTokens: 12}}
	if _, _, err := repository.InsertUsageEvents(db, events); err != nil {
		t.Fatalf("insert overview failure event: %v", err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_background_overview
		BEFORE INSERT ON usage_overview_hourly_stats
		BEGIN
			SELECT RAISE(ABORT, 'forced background overview failure');
		END`).Error; err != nil {
		t.Fatalf("create overview failure trigger: %v", err)
	}
	runner := poller.NewUsageAggregationRunner(db, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	// 执行：依靠 startup wake 运行后台循环，等待 Activity 与 Identity 越过失败的 Overview。
	go func() {
		done <- runner.Run(ctx)
	}()
	waitForUsageAggregationRunnerCondition(t, func() bool {
		var activityCheckpoint entities.UsageActivityAggregationCheckpoint
		if err := db.Where("name = ?", "activity").Take(&activityCheckpoint).Error; err != nil || activityCheckpoint.LastAggregatedUsageEventID != 1 {
			return false
		}
		var stored entities.UsageIdentity
		return db.First(&stored, identity.ID).Error == nil && stored.TotalRequests == 1 && stored.CachedTokens == 7 && stored.LastAggregatedUsageEventID == 1
	})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runner Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not stop after overview failure isolation")
	}

	// 断言：runner 完全停止后，Overview 事务已回滚，Activity 和 Identity 的独立最终结果已经提交。
	assertUsageAggregationCheckpointMissing(t, db, "overview")
	assertUsageAggregationCheckpoint(t, db, "activity", 1)
}

func TestUsageAggregationRunnerRunKeepsOverviewAndActivityMovingAfterIdentityFailure(t *testing.T) {
	// 准备：写入同属三个聚合的事件和 identity，并让真正增加请求数的 Identity UPDATE 持续失败。
	db := openUsageAggregationRunnerDatabase(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	identity := entities.UsageIdentity{Name: "runner-identity-failure", AuthType: entities.UsageIdentityAuthTypeAuthFile, Identity: "runner-identity-failure", Type: "codex"}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatalf("insert identity failure identity: %v", err)
	}
	events := []entities.UsageEvent{{EventKey: "runner-identity-failure", APIGroupKey: "provider-a", Model: "model-a", AuthType: "oauth", AuthIndex: identity.Identity, Timestamp: now.Add(-time.Minute), InputTokens: 10, CachedTokens: 7, CacheReadTokens: 2, TotalTokens: 12}}
	if _, _, err := repository.InsertUsageEvents(db, events); err != nil {
		t.Fatalf("insert identity failure event: %v", err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_background_identity
		BEFORE UPDATE ON usage_identities
		WHEN NEW.total_requests > OLD.total_requests
		BEGIN
			SELECT RAISE(ABORT, 'forced background identity failure');
		END`).Error; err != nil {
		t.Fatalf("create identity failure trigger: %v", err)
	}
	runner := poller.NewUsageAggregationRunner(db, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	// 执行：依靠 startup wake 运行后台循环，等待 Overview 与 Activity 各自追平 event cursor。
	go func() {
		done <- runner.Run(ctx)
	}()
	waitForUsageAggregationRunnerCondition(t, func() bool {
		var overviewCheckpoint entities.UsageOverviewAggregationCheckpoint
		if err := db.Where("name = ?", "overview").Take(&overviewCheckpoint).Error; err != nil || overviewCheckpoint.LastAggregatedUsageEventID != 1 {
			return false
		}
		var activityCheckpoint entities.UsageActivityAggregationCheckpoint
		return db.Where("name = ?", "activity").Take(&activityCheckpoint).Error == nil && activityCheckpoint.LastAggregatedUsageEventID == 1
	})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runner Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not stop after identity failure isolation")
	}

	// 断言：runner 完全停止后，两个独立 checkpoint 已提交，Identity 旧字段和每行 cursor 随失败事务回滚。
	assertUsageAggregationCheckpoint(t, db, "overview", 1)
	assertUsageAggregationCheckpoint(t, db, "activity", 1)
	var stored entities.UsageIdentity
	if err := db.First(&stored, identity.ID).Error; err != nil {
		t.Fatalf("load failed identity row: %v", err)
	}
	if stored.TotalRequests != 0 || stored.CachedTokens != 0 || stored.LastAggregatedUsageEventID != 0 {
		t.Fatalf("identity failure changed old row: %+v", stored)
	}
}

func TestUsageAggregationRunnerRunWakesAfterIdleNotification(t *testing.T) {
	// 准备：先启动空数据库 runner，让它完成一轮空调度并进入等待。
	db := openUsageAggregationRunnerDatabase(t)
	runner := poller.NewUsageAggregationRunner(db, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runner.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	time.Sleep(50 * time.Millisecond)

	// 执行：空闲后提交事件并连续通知多次，容量 1 wake 必须合并且不能阻塞调用方。
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	events := []entities.UsageEvent{{EventKey: "runner-idle-wake", APIGroupKey: "provider-a", Model: "model-a", Timestamp: now.Add(-time.Minute), InputTokens: 10, TotalTokens: 10}}
	if _, _, err := repository.InsertUsageEvents(db, events); err != nil {
		t.Fatalf("insert idle wake event: %v", err)
	}
	var storedEvents []entities.UsageEvent
	if err := db.Order("id asc").Find(&storedEvents).Error; err != nil {
		t.Fatalf("load idle wake event: %v", err)
	}
	for index := 0; index < 10; index++ {
		runner.NotifyUsageEventsCommitted(storedEvents, nil)
	}
	waitForUsageAggregationRunnerCondition(t, func() bool {
		var checkpoint entities.UsageActivityAggregationCheckpoint
		return db.Where("name = ?", "activity").Take(&checkpoint).Error == nil && checkpoint.LastAggregatedUsageEventID == 1
	})

	// 断言：一次合并 wake 最终追平 Overview 和 Activity 两个独立 checkpoint。
	assertUsageAggregationCheckpoint(t, db, "overview", 1)
	assertUsageAggregationCheckpoint(t, db, "activity", 1)
}

type recordingUsageAggregationHeaderAppender struct {
	// callCount 记录 TryAppend 调用次数。
	callCount int
	// snapshots 保存所有成功投递的快照。
	snapshots []quota.UsageHeaderSnapshot
	// accept 控制测试 appender 是否接受本次投递。
	accept bool
}

type acceptThirdUsageAggregationHeaderAppender struct {
	// calls 原子记录后台自动投递次数，供测试 goroutine 安全观察。
	calls atomic.Int64
	// accepted 原子记录最终成功接收的 snapshot 数量。
	accepted atomic.Int64
}

func (a *acceptThirdUsageAggregationHeaderAppender) TryAppendUsageHeaderSnapshots(snapshots []quota.UsageHeaderSnapshot) bool {
	// 前两次固定拒绝，复现单次 wake 内自然轮转仍无法成功的场景。
	if a.calls.Add(1) < 3 {
		return false
	}
	// 第三次接受整个 ready batch，并原子记录实际数量。
	a.accepted.Add(int64(len(snapshots)))
	return true
}

func (a *recordingUsageAggregationHeaderAppender) TryAppendUsageHeaderSnapshots(snapshots []quota.UsageHeaderSnapshot) bool {
	// 每次调用都记录尝试次数，便于断言队列满后的重试。
	a.callCount++
	// 拒绝时不复制 snapshots，模拟 quota worker channel 已满。
	if !a.accept {
		return false
	}
	// 接受时复制输入，避免 runner 后续修改切片。
	a.snapshots = append(a.snapshots, snapshots...)
	return true
}

func waitForUsageAggregationRunnerCondition(t *testing.T, condition func() bool) {
	// 准备：统一设置小型集成测试的最长等待时间和轮询间隔。
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	// 执行：只轮询少量 SQLite 行，不运行任何性能压测。
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 断言：超时表示后台 runner 没有完成预期状态转换。
	t.Fatal("timed out waiting for usage aggregation runner condition")
}

func openUsageAggregationRunnerDatabase(t *testing.T) *gorm.DB {
	// 每个 runner 用例使用独立真实 SQLite 文件和项目单连接配置。
	t.Helper()
	db, err := repository.OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "usage-aggregation-runner.db")})
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		if dbErr != nil {
			t.Errorf("load sql db: %v", dbErr)
			return
		}
		if closeErr := sqlDB.Close(); closeErr != nil {
			t.Errorf("close sql db: %v", closeErr)
		}
	})
	return db
}

func assertUsageAggregationCheckpoint(t *testing.T, db *gorm.DB, name string, want int64) {
	// Overview 和 Activity 使用不同实体表，但都按固定 name 断言最终 cursor。
	t.Helper()
	var got int64
	table := "usage_overview_aggregation_checkpoints"
	if name == "activity" {
		table = "usage_activity_aggregation_checkpoints"
	}
	if err := db.Table(table).Where("name = ?", name).Pluck("last_aggregated_usage_event_id", &got).Error; err != nil {
		t.Fatalf("load %s checkpoint: %v", name, err)
	}
	if got != want {
		t.Fatalf("expected %s checkpoint %d, got %d", name, want, got)
	}
}

func assertUsageAggregationCheckpointMissing(t *testing.T, db *gorm.DB, name string) {
	// 缺失 checkpoint 证明对应事务尚未获得 writer 或已完整回滚。
	t.Helper()
	table := "usage_overview_aggregation_checkpoints"
	if name == "activity" {
		table = "usage_activity_aggregation_checkpoints"
	}
	var count int64
	if err := db.Table(table).Where("name = ?", name).Count(&count).Error; err != nil {
		t.Fatalf("count %s checkpoint: %v", name, err)
	}
	if count != 0 {
		t.Fatalf("expected %s checkpoint to be missing, got %d rows", name, count)
	}
}
