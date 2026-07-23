package quota

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/timeutil"

	"gorm.io/gorm"
)

type ServiceOptions struct {
	RefreshWorkerLimit               int
	UsageHeaderSnapshotFlushInterval time.Duration
	PricingCatalog                   *pricing.Catalog
}

const usageHeaderSnapshotQueueSize = 100

type Service struct {
	db       *gorm.DB
	registry ProviderRegistry
	pricing  *pricing.Catalog

	refreshMu    sync.Mutex
	refreshTasks map[string]*RefreshTaskRecord
	// resetInFlight 按 auth_index 记录正在消费的 reset credit，避免并发重复扣减官方次数。
	resetMu       sync.Mutex
	resetInFlight map[string]struct{}
	// inspectionCompletedAt 只记录用户手动启动巡检后，该巡检轮次完成的时间。
	inspectionCompletedAt       time.Time
	inspectionRoundActive       bool
	inspectionRoundAuthIndexSet map[string]struct{}
	refreshWorkerTokens         chan struct{}
	refreshTaskTTL              time.Duration
	refreshCooldown             func(time.Duration)
	refreshContext              context.Context
	refreshCancel               context.CancelFunc
	// autoRefreshMu 保护 autoRefreshRunning，避免多个 tick 同时启动扫描。
	autoRefreshMu sync.Mutex
	// autoRefreshRunning 表示上一轮自动刷新还有 queued/running 任务未完全结束。
	autoRefreshRunning bool
	// autoRefreshSettingsChanged 用于设置保存后唤醒调度循环，使长周期配置变更不必等到旧触发时间。
	autoRefreshSettingsChanged chan struct{}
	// autoRefreshNow 和 autoRefreshDelay 仅用于调度器测试固定时间和跳过真实等待。
	autoRefreshNow   func() time.Time
	autoRefreshDelay func(context.Context, time.Duration) bool
	// lastAutoRefreshAttemptAt 记录最近一次尝试启动整轮自动刷新的时间，用于扫描失败退避。
	lastAutoRefreshAttemptAt time.Time
	// lastAutoRefreshRoundAt 记录上次启动整轮 Auth Files 自动刷新入队的内存时间。
	lastAutoRefreshRoundAt time.Time
	// refreshLifecycleMu 保护 refreshClosing 和 refreshWG.Add，避免关闭等待期间继续登记后台 goroutine。
	refreshLifecycleMu sync.Mutex
	// refreshClosing 表示 App 正在关闭 quota 后台任务，后续刷新请求不能再派生新 goroutine。
	refreshClosing bool
	// refreshWG 跟踪 service 派生的 dispatcher/worker/scheduler goroutine，App 关闭 DB 前会等待它们退出。
	refreshWG sync.WaitGroup

	usageHeaderCh            chan []UsageHeaderSnapshot
	usageHeaderSlots         chan struct{}
	usageHeaderStopCh        chan struct{}
	usageHeaderDoneCh        chan struct{}
	usageHeaderFlushInterval time.Duration
	usageHeaderMu            sync.Mutex
	usageHeaderClosing       bool
	usageHeaderCloseOnce     sync.Once

	observationRecorder *quotaObservationRecorder
}

type CheckRequest struct {
	AuthIndex string `json:"auth_index"`
}

type CheckResponse struct {
	ID                                  string     `json:"id"`
	Quota                               []QuotaRow `json:"quota"`
	RateLimitResetCreditsAvailableCount *int       `json:"rateLimitResetCreditsAvailableCount,omitempty"`
}

func NewService(db *gorm.DB, caller ManagementAPICaller, pricingCatalog *pricing.Catalog) *Service {
	return NewServiceWithOptions(db, caller, ServiceOptions{PricingCatalog: pricingCatalog})
}

func NewServiceWithOptions(db *gorm.DB, caller ManagementAPICaller, options ServiceOptions) *Service {
	return NewServiceWithRegistryAndOptions(db, NewDefaultProviderRegistry(caller, DefaultProviderConfigs()), options)
}

func NewServiceWithRegistry(db *gorm.DB, registry ProviderRegistry, pricingCatalog *pricing.Catalog) *Service {
	return NewServiceWithRegistryAndOptions(db, registry, ServiceOptions{PricingCatalog: pricingCatalog})
}

func NewServiceWithRegistryAndOptions(db *gorm.DB, registry ProviderRegistry, options ServiceOptions) *Service {
	workerLimit := options.RefreshWorkerLimit
	if workerLimit <= 0 {
		workerLimit = RefreshWorkerLimit
	}
	if workerLimit > 100 {
		workerLimit = 100
	}
	usageHeaderFlushInterval := options.UsageHeaderSnapshotFlushInterval
	if usageHeaderFlushInterval <= 0 {
		usageHeaderFlushInterval = usageHeaderSnapshotFlushInterval
	}
	refreshContext, refreshCancel := context.WithCancel(context.Background())
	pricingCatalog := options.PricingCatalog
	if pricingCatalog == nil {
		panic("pricing catalog is required")
	}
	service := &Service{
		db:                         db,
		registry:                   registry,
		pricing:                    pricingCatalog,
		refreshTasks:               make(map[string]*RefreshTaskRecord),
		resetInFlight:              make(map[string]struct{}),
		refreshWorkerTokens:        make(chan struct{}, workerLimit),
		refreshTaskTTL:             RefreshTransientTaskTTL,
		refreshCooldown:            time.Sleep,
		refreshContext:             refreshContext,
		refreshCancel:              refreshCancel,
		autoRefreshSettingsChanged: make(chan struct{}, 1),
		usageHeaderCh:              make(chan []UsageHeaderSnapshot, usageHeaderSnapshotQueueSize),
		usageHeaderSlots:           make(chan struct{}, usageHeaderSnapshotQueueSize),
		usageHeaderStopCh:          make(chan struct{}),
		usageHeaderDoneCh:          make(chan struct{}),
		usageHeaderFlushInterval:   usageHeaderFlushInterval,
	}
	for i := 0; i < usageHeaderSnapshotQueueSize; i++ {
		service.usageHeaderSlots <- struct{}{}
	}
	service.observationRecorder = newQuotaObservationRecorder(
		repositoryQuotaObservationStore{db: db},
		pricingCatalog,
		quotaObservationQueueSize,
	)
	go service.runUsageHeaderSnapshotWorker()
	return service
}

func (s *Service) SetRefreshContext(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Service 自己持有一层 cancel，App.Close 即使没有外部 cancel 也能停止正在等待的刷新任务。
	nextContext, nextCancel := context.WithCancel(ctx)
	s.refreshLifecycleMu.Lock()
	previousCancel := s.refreshCancel
	if s.refreshClosing {
		s.refreshLifecycleMu.Unlock()
		nextCancel()
		return
	}
	s.refreshContext = nextContext
	s.refreshCancel = nextCancel
	s.refreshLifecycleMu.Unlock()
	if previousCancel != nil {
		// 替换父 context 时取消旧租约，避免旧 worker 继续挂在不可关闭的 context 上。
		previousCancel()
	}
}

func (s *Service) startRefreshGoroutine(fn func()) bool {
	if s == nil {
		return false
	}
	s.refreshLifecycleMu.Lock()
	defer s.refreshLifecycleMu.Unlock()
	if s.refreshClosing {
		return false
	}
	s.refreshWG.Add(1)
	go func() {
		defer s.refreshWG.Done()
		fn()
	}()
	return true
}

func (s *Service) WaitRefreshTasks() {
	if s == nil {
		return
	}
	s.refreshWG.Wait()
}

func (s *Service) refreshContextSnapshot() context.Context {
	if s == nil {
		return context.Background()
	}
	s.refreshLifecycleMu.Lock()
	defer s.refreshLifecycleMu.Unlock()
	if s.refreshContext == nil {
		return context.Background()
	}
	return s.refreshContext
}

func (s *Service) StopRefreshTasks() {
	if s == nil {
		return
	}
	s.refreshLifecycleMu.Lock()
	// 先封住新的后台 goroutine 登记，再等待已登记任务退出，避免 WaitGroup Add/Wait 并发。
	s.refreshClosing = true
	cancel := s.refreshCancel
	s.refreshLifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.stopUsageHeaderSnapshotWorker()
	s.refreshWG.Wait()
	s.observationRecorder.stop()
}

func (s *Service) Check(ctx context.Context, request CheckRequest) (CheckResponse, error) {
	snapshot, err := s.checkWithSnapshot(ctx, request)
	if err != nil {
		return CheckResponse{}, err
	}
	return snapshot.response, nil
}

type quotaCheckSnapshot struct {
	response   CheckResponse
	identity   entities.UsageIdentity
	provider   string
	observedAt time.Time
}

func (s *Service) checkWithSnapshot(ctx context.Context, request CheckRequest) (quotaCheckSnapshot, error) {
	// 单条查询以 auth_index 为唯一入口，前端不需要知道具体 provider 的 API 细节。
	authIndex := strings.TrimSpace(request.AuthIndex)
	if authIndex == "" {
		return quotaCheckSnapshot{}, fmt.Errorf("%w: auth_index is required", ErrValidation)
	}
	// 只允许 auth files 身份查询限额，AI provider 身份不进入 provider 调用链路。
	identity, err := repository.GetActiveAuthFileUsageIdentityByAuthIndex(ctx, s.db, authIndex)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return quotaCheckSnapshot{}, fmt.Errorf("%w: %s", ErrNotFound, authIndex)
		}
		return quotaCheckSnapshot{}, err
	}
	// 按相邻项目规则先匹配 provider 再匹配 type，解析出实际要调用的 quota handler。
	resolvedProvider, handler, ok := s.resolveQuotaHandlerForIdentity(identity)
	if !ok {
		return quotaCheckSnapshot{}, fmt.Errorf("%w: %s", ErrUnsupportedType, normalizeIdentityType(identity.Provider))
	}
	// provider 返回各自原始结构后，再统一转换为前端可复用的 quota rows。
	providerOutput, err := handler.Check(ctx, ProviderInput{Identity: identity})
	if err != nil {
		return quotaCheckSnapshot{}, err
	}
	provider := normalizeIdentityType(providerOutput.Provider)
	if provider == "" {
		provider = resolvedProvider
	}
	observedAt := timeutil.NormalizeStorageTime(time.Now())
	response := CheckResponse{
		ID:    authIndex,
		Quota: NormalizeQuotaRows(providerOutput),
	}
	// reset 次数跟随官方刷新结果写入同一份限额缓存，前端只展示缓存里的官方值。
	if count, ok := rateLimitResetCreditsAvailableCount(providerOutput); ok {
		response.RateLimitResetCreditsAvailableCount = count
	}
	return quotaCheckSnapshot{
		response:   response,
		identity:   identity,
		provider:   provider,
		observedAt: observedAt,
	}, nil
}

func (s *Service) resolveQuotaHandler(provider string, identityType string) (string, ProviderHandler, bool) {
	for _, candidate := range resolveQuotaIdentityTypes(provider, identityType) {
		if handler, ok := s.registry.Provider(candidate); ok {
			return candidate, handler, true
		}
	}
	return "", nil, false
}

func (s *Service) resolveQuotaHandlerForIdentity(identity entities.UsageIdentity) (string, ProviderHandler, bool) {
	return s.resolveQuotaHandler(identity.Provider, identity.Type)
}

func resolveQuotaIdentityTypes(provider string, identityType string) []string {
	candidates := make([]string, 0, 2)
	for _, value := range []string{provider, identityType} {
		normalized := normalizeIdentityType(value)
		if normalized == "" || slices.Contains(candidates, normalized) {
			continue
		}
		candidates = append(candidates, normalized)
	}
	return candidates
}

func rateLimitResetCreditsAvailableCount(output ProviderOutput) (*int, bool) {
	// 目前只有 Codex 返回 reset credit；其它 provider 保持字段缺省，避免误显示 reset 按钮。
	switch result := output.Result.(type) {
	case CodexResult:
		if result.Usage == nil || result.Usage.RateLimitResetCredits == nil || result.Usage.RateLimitResetCredits.AvailableCount == nil {
			return nil, false
		}
		return result.Usage.RateLimitResetCredits.AvailableCount, true
	case *CodexResult:
		if result == nil || result.Usage == nil || result.Usage.RateLimitResetCredits == nil || result.Usage.RateLimitResetCredits.AvailableCount == nil {
			return nil, false
		}
		return result.Usage.RateLimitResetCredits.AvailableCount, true
	default:
		return nil, false
	}
}
