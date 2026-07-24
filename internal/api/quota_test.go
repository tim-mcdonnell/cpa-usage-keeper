package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"cpa-usage-keeper/internal/auth"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/quota"
	"cpa-usage-keeper/internal/quota/estimate"
)

type quotaProviderStub struct {
	resetRequest             quota.ResetRequest
	resetResponse            quota.ResetResponse
	resetErr                 error
	refreshRequest           quota.RefreshRequest
	refreshResponse          quota.RefreshResponse
	refreshErr               error
	taskAuthIndex            string
	taskResponse             quota.RefreshTaskResponse
	taskErr                  error
	cacheRequest             quota.CacheRequest
	cacheResponse            quota.CacheResponse
	cacheErr                 error
	capacityRequest          quota.CapacityRequest
	capacityResponse         quota.CapacityResponse
	capacityErr              error
	capacityDetailRequest    quota.CapacityDetailRequest
	capacityDetailResponse   quota.CapacityDetailResponse
	capacityDetailErr        error
	observationRequest       quota.ObservationSeriesRequest
	observationResponse      quota.ObservationSeriesResponse
	observationErr           error
	inspectionStatusResponse quota.InspectionStatus
	inspectionStatusErr      error
	inspectionStartResponse  quota.InspectionStatus
	inspectionStartErr       error
	inspectionStatusCalls    int
	inspectionStartCalls     int
}

func (s *quotaProviderStub) GetCapacity(ctx context.Context, request quota.CapacityRequest) (quota.CapacityResponse, error) {
	s.capacityRequest = request
	if s.capacityErr != nil {
		return quota.CapacityResponse{}, s.capacityErr
	}
	return s.capacityResponse, nil
}

func (s *quotaProviderStub) GetCapacityDetail(ctx context.Context, request quota.CapacityDetailRequest) (quota.CapacityDetailResponse, error) {
	s.capacityDetailRequest = request
	if s.capacityDetailErr != nil {
		return quota.CapacityDetailResponse{}, s.capacityDetailErr
	}
	return s.capacityDetailResponse, nil
}

func (s *quotaProviderStub) ListObservations(ctx context.Context, request quota.ObservationSeriesRequest) (quota.ObservationSeriesResponse, error) {
	s.observationRequest = request
	if s.observationErr != nil {
		return quota.ObservationSeriesResponse{}, s.observationErr
	}
	return s.observationResponse, nil
}

func (s *quotaProviderStub) GetResetCredits(ctx context.Context, request quota.ResetCreditsRequest) (quota.ResetCreditsResponse, error) {
	return quota.ResetCreditsResponse{}, nil
}

func (s *quotaProviderStub) Refresh(ctx context.Context, request quota.RefreshRequest) (quota.RefreshResponse, error) {
	s.refreshRequest = request
	if s.refreshErr != nil {
		return quota.RefreshResponse{}, s.refreshErr
	}
	return s.refreshResponse, nil
}

func (s *quotaProviderStub) GetRefreshTaskByAuthIndex(ctx context.Context, authIndex string) (quota.RefreshTaskResponse, error) {
	s.taskAuthIndex = authIndex
	if s.taskErr != nil {
		return quota.RefreshTaskResponse{}, s.taskErr
	}
	return s.taskResponse, nil
}

func (s *quotaProviderStub) GetCachedQuota(ctx context.Context, request quota.CacheRequest) (quota.CacheResponse, error) {
	s.cacheRequest = request
	if s.cacheErr != nil {
		return quota.CacheResponse{}, s.cacheErr
	}
	return s.cacheResponse, nil
}

func (s *quotaProviderStub) GetInspectionStatus(ctx context.Context) (quota.InspectionStatus, error) {
	s.inspectionStatusCalls++
	if s.inspectionStatusErr != nil {
		return quota.InspectionStatus{}, s.inspectionStatusErr
	}
	return s.inspectionStatusResponse, nil
}

func (s *quotaProviderStub) Reset(ctx context.Context, request quota.ResetRequest) (quota.ResetResponse, error) {
	s.resetRequest = request
	if s.resetErr != nil {
		return quota.ResetResponse{}, s.resetErr
	}
	return s.resetResponse, nil
}

func (s *quotaProviderStub) StartInspection(ctx context.Context) (quota.InspectionStatus, error) {
	s.inspectionStartCalls++
	if s.inspectionStartErr != nil {
		return quota.InspectionStatus{}, s.inspectionStartErr
	}
	return s.inspectionStartResponse, nil
}

func (s *quotaProviderStub) GetAutoRefreshSettings(ctx context.Context) (quota.AutoRefreshSettings, error) {
	return quota.AutoRefreshSettings{}, nil
}

func (s *quotaProviderStub) UpdateAutoRefreshSettings(ctx context.Context, settings quota.AutoRefreshSettings) (quota.AutoRefreshSettings, error) {
	return settings, nil
}

func TestQuotaCacheReturnsCachedCurrentPageQuota(t *testing.T) {
	refreshedAt := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	provider := &quotaProviderStub{cacheResponse: quota.CacheResponse{
		Items: []quota.CachedQuotaItem{{AuthIndex: "auth-1", FileName: apiStringPtr("claude-user.json"), Status: quota.RefreshTaskStatusCompleted, RefreshedAt: &refreshedAt, Quota: &quota.CheckResponse{ID: "auth-1", Quota: []quota.QuotaRow{{Key: "rate_limit.secondary_window", Label: "Weekly", PlanType: "plus"}}}}},
	}}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/quota/cache", strings.NewReader(`{"auth_indexes":["auth-1","auth-2"]}`))

	req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	if got := strings.Join(provider.cacheRequest.AuthIndexes, ","); got != "auth-1,auth-2" {
		t.Fatalf("expected auth indexes to be forwarded, got %+v", provider.cacheRequest.AuthIndexes)
	}
	body := resp.Body.String()
	if !contains(body, `"items"`) || !contains(body, `"file_name":"claude-user.json"`) || !contains(body, `"refreshed_at":"2026-05-26T12:00:00Z"`) || contains(body, `"updated_at"`) || !contains(body, `"id":"auth-1"`) || !contains(body, `"label":"Weekly"`) || !contains(body, `"planType":"plus"`) {
		t.Fatalf("unexpected response body: %s", body)
	}
}

func TestQuotaCapacityForwardsBatchShapeAndReturnsEmptyVersusInsufficient(t *testing.T) {
	provider := &quotaProviderStub{capacityResponse: quota.CapacityResponse{
		Items: []quota.CredentialCapacity{
			{AuthIndex: "auth-empty", Windows: []quota.CapacityWindow{}},
			{
				AuthIndex: "auth-data",
				Windows: []quota.CapacityWindow{{
					Provider:      "codex",
					WindowKindID:  estimate.WindowKindCodexFiveHour,
					WindowSeconds: 18000,
					CurrentEpoch: &estimate.WindowEstimate{
						Confidence: estimate.ConfidenceInsufficient,
						Points: []estimate.PointDiagnostic{{
							ObservationID: 7,
							Class:         estimate.PointEpochUnassigned,
						}},
						Method: estimate.MethodOLSBlockBootstrap,
					},
					RecentEpochs: []estimate.WindowEstimate{},
				}},
			},
		},
	}}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/quota/capacity",
		strings.NewReader(`{"auth_indexes":["auth-empty","auth-data"]}`),
	)
	req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	if got := strings.Join(provider.capacityRequest.AuthIndexes, ","); got != "auth-empty,auth-data" {
		t.Fatalf("capacity auth indexes = %q", got)
	}
	body := resp.Body.String()
	if !contains(body, `"auth_index":"auth-empty","windows":[]`) ||
		!contains(body, `"confidence":"insufficient"`) ||
		!contains(body, `"epoch_reset_at":null`) ||
		!contains(body, `"recent_epochs":[]`) {
		t.Fatalf("unexpected capacity response: %s", body)
	}
}

func TestQuotaCapacityDetailReturnsClassificationsObservationsAndExactFittedSeries(t *testing.T) {
	resetAt := time.Date(2026, 7, 23, 15, 0, 0, 0, time.UTC)
	provider := &quotaProviderStub{capacityDetailResponse: quota.CapacityDetailResponse{
		Estimate: estimate.WindowEstimate{
			Provider:     "codex",
			WindowKindID: estimate.WindowKindCodexFiveHour,
			EpochResetAt: &resetAt,
			Confidence:   estimate.ConfidenceLow,
			Points: []estimate.PointDiagnostic{{
				ObservationID:           7,
				Class:                   estimate.PointCoverageGapInterval,
				CumulativePercentOffset: 2,
			}},
			FittedSeries: []estimate.FittedPoint{{
				ObservationID:           7,
				AttributedTokens:        100,
				RawUsedPercent:          12,
				AdjustedUsedPercent:     10,
				CumulativePercentOffset: 2,
				FittedPercent:           10,
			}},
			Method: estimate.MethodOLSBlockBootstrap,
		},
		Observations: []entities.QuotaObservation{{
			ID:           7,
			AuthIndex:    "auth-1",
			WindowKindID: estimate.WindowKindCodexFiveHour,
		}},
		Epochs: []estimate.WindowEstimate{{
			Provider:      "codex",
			WindowKindID:  estimate.WindowKindCodexFiveHour,
			WindowSeconds: 18000,
			EpochResetAt:  &resetAt,
			Confidence:    estimate.ConfidenceLow,
			Method:        estimate.MethodOLSBlockBootstrap,
		}},
	}}
	path := "/api/v1/quota/capacity/detail?auth_index=auth-1" +
		"&window_kind_id=codex%2Foverall%2Frate_limit%2F18000" +
		"&epoch_reset_at=2026-07-23T15%3A00%3A00Z"
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})
	req := httptest.NewRequest(http.MethodGet, path, nil)
	resp := httptest.NewRecorder()

	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	if provider.capacityDetailRequest.AuthIndex != "auth-1" ||
		provider.capacityDetailRequest.WindowKindID != estimate.WindowKindCodexFiveHour ||
		provider.capacityDetailRequest.EpochResetAt == nil ||
		!provider.capacityDetailRequest.EpochResetAt.Equal(resetAt) {
		t.Fatalf("unexpected capacity detail request: %+v", provider.capacityDetailRequest)
	}
	body := resp.Body.String()
	if !contains(body, `"class":"coverage_gap_interval"`) ||
		!contains(body, `"cumulative_percent_offset":2`) ||
		!contains(body, `"adjusted_used_percent":10`) ||
		!contains(body, `"observations":[{"id":7`) ||
		!contains(body, `"epochs":[{"provider":"codex","window_kind_id":"codex/overall/rate_limit/18000"`) {
		t.Fatalf("unexpected capacity detail response: %s", body)
	}
}

func TestQuotaCapacityRejectsMalformedRequestsAndUsesStandardErrors(t *testing.T) {
	testCases := []struct {
		name     string
		method   string
		path     string
		body     string
		provider *quotaProviderStub
		status   int
		error    string
	}{
		{
			name:     "batch malformed JSON",
			method:   http.MethodPost,
			path:     "/api/v1/quota/capacity",
			body:     `{`,
			provider: &quotaProviderStub{},
			status:   http.StatusBadRequest,
			error:    "auth_indexes are required",
		},
		{
			name:     "detail missing window",
			method:   http.MethodGet,
			path:     "/api/v1/quota/capacity/detail?auth_index=auth-1",
			provider: &quotaProviderStub{},
			status:   http.StatusBadRequest,
			error:    "capacity detail parameters are invalid",
		},
		{
			name:     "detail malformed epoch",
			method:   http.MethodGet,
			path:     "/api/v1/quota/capacity/detail?auth_index=auth-1&window_kind_id=kind&epoch_reset_at=nope",
			provider: &quotaProviderStub{},
			status:   http.StatusBadRequest,
			error:    "capacity detail parameters are invalid",
		},
		{
			name:     "batch provider failure",
			method:   http.MethodPost,
			path:     "/api/v1/quota/capacity",
			body:     `{"auth_indexes":["auth-1"]}`,
			provider: &quotaProviderStub{capacityErr: errors.New("database unavailable")},
			status:   http.StatusInternalServerError,
			error:    "internal server error",
		},
		{
			name:     "detail not found",
			method:   http.MethodGet,
			path:     "/api/v1/quota/capacity/detail?auth_index=auth-1&window_kind_id=kind",
			provider: &quotaProviderStub{capacityDetailErr: quota.ErrNotFound},
			status:   http.StatusNotFound,
			error:    "quota capacity not found",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: testCase.provider})
			req := httptest.NewRequest(testCase.method, testCase.path, strings.NewReader(testCase.body))
			if testCase.method == http.MethodPost {
				req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
				req.Header.Set("Content-Type", "application/json")
			}
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if resp.Code != testCase.status || !contains(resp.Body.String(), `"error":"`+testCase.error+`"`) {
				t.Fatalf("status=%d body=%s, want status=%d error=%q", resp.Code, resp.Body.String(), testCase.status, testCase.error)
			}
		})
	}
}

func TestQuotaCapacityEnforcesCredentialAndBodyLimitsBeforeProvider(t *testing.T) {
	authIndexes := make([]string, quota.CapacityMaxAuthIndexes+1)
	for index := range authIndexes {
		authIndexes[index] = "auth-" + strconv.Itoa(index)
	}
	bodyBytes, err := json.Marshal(map[string]any{"auth_indexes": authIndexes})
	if err != nil {
		t.Fatalf("marshal auth indexes: %v", err)
	}
	testCases := []struct {
		name         string
		body         string
		wantStatus   int
		wantError    string
		wantProvider bool
	}{
		{
			name:         "exactly 100 credentials",
			body:         mustMarshalCapacityAuthIndexes(t, authIndexes[:quota.CapacityMaxAuthIndexes]),
			wantStatus:   http.StatusOK,
			wantProvider: true,
		},
		{
			name:       "101 credentials",
			body:       string(bodyBytes),
			wantStatus: http.StatusBadRequest,
			wantError:  "at most 100 auth_indexes per request",
		},
		{
			name: "exactly 64 KiB",
			body: func() string {
				prefix := `{"auth_indexes":["auth-1"]}`
				return prefix + strings.Repeat(" ", capacityRequestBodyLimit-len(prefix))
			}(),
			wantStatus:   http.StatusOK,
			wantProvider: true,
		},
		{
			name: "over 64 KiB",
			body: func() string {
				prefix := `{"auth_indexes":["auth-1"]}`
				return prefix + strings.Repeat(" ", capacityRequestBodyLimit-len(prefix)+1)
			}(),
			wantStatus: http.StatusRequestEntityTooLarge,
			wantError:  "request body too large",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			provider := &quotaProviderStub{}
			router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/quota/capacity",
				strings.NewReader(testCase.body),
			)
			req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()

			router.ServeHTTP(resp, req)

			if resp.Code != testCase.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", resp.Code, resp.Body.String(), testCase.wantStatus)
			}
			if testCase.wantError != "" &&
				resp.Body.String() != `{"error":"`+testCase.wantError+`"}` {
				t.Fatalf("body = %s, want exact error %q", resp.Body.String(), testCase.wantError)
			}
			called := len(provider.capacityRequest.AuthIndexes) > 0
			if called != testCase.wantProvider {
				t.Fatalf("provider called = %v, want %v", called, testCase.wantProvider)
			}
		})
	}
}

func mustMarshalCapacityAuthIndexes(t *testing.T, authIndexes []string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"auth_indexes": authIndexes})
	if err != nil {
		t.Fatalf("marshal capacity auth indexes: %v", err)
	}
	return string(body)
}

func TestQuotaCapacityEndpointsRejectAPIKeyViewer(t *testing.T) {
	sessions := auth.NewSessionManager(time.Hour)
	viewerToken, _, err := sessions.CreateAPIKeyViewer(42)
	if err != nil {
		t.Fatalf("CreateAPIKeyViewer returned error: %v", err)
	}
	config := AuthConfig{Enabled: true, LoginPassword: "secret", SessionTTL: time.Hour}
	provider := &quotaProviderStub{}
	router := NewRouter(nil, nil, nil, nil, config, NewAuthHandler(config, sessions), "", OptionalProviders{Quota: provider})
	testCases := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodPost, path: "/api/v1/quota/capacity", body: `{"auth_indexes":["auth-1"]}`},
		{method: http.MethodGet, path: "/api/v1/quota/capacity/detail?auth_index=auth-1&window_kind_id=kind"},
	}
	for _, testCase := range testCases {
		req := httptest.NewRequest(testCase.method, testCase.path, strings.NewReader(testCase.body))
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: viewerToken})
		if testCase.method == http.MethodPost {
			req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
			req.Header.Set("Content-Type", "application/json")
		}
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != http.StatusForbidden {
			t.Fatalf("%s %s returned %d body=%s", testCase.method, testCase.path, resp.Code, resp.Body.String())
		}
	}
	if len(provider.capacityRequest.AuthIndexes) != 0 || provider.capacityDetailRequest.AuthIndex != "" {
		t.Fatalf("provider was called for API key viewer: batch=%+v detail=%+v", provider.capacityRequest, provider.capacityDetailRequest)
	}
}

func TestQuotaObservationsForwardsRequiredSeriesAndReturnsTruncationMarker(t *testing.T) {
	observedAt := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	provider := &quotaProviderStub{observationResponse: quota.ObservationSeriesResponse{
		Items: []entities.QuotaObservation{{
			ID:           1,
			AuthIndex:    "auth-1",
			WindowKindID: "codex/overall/rate_limit/18000",
			ObservedAt:   observedAt,
		}},
		Truncated: true,
	}}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/quota/observations?auth_index=auth-1&window_kind_id=codex%2Foverall%2Frate_limit%2F18000&start=2026-07-01T00%3A00%3A00Z&end=2026-07-24T00%3A00%3A00Z",
		nil,
	)
	resp := httptest.NewRecorder()

	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	if provider.observationRequest.AuthIndex != "auth-1" ||
		provider.observationRequest.WindowKindID != "codex/overall/rate_limit/18000" ||
		provider.observationRequest.Start.IsZero() ||
		provider.observationRequest.End.IsZero() {
		t.Fatalf("unexpected observation request: %+v", provider.observationRequest)
	}
	body := resp.Body.String()
	if !contains(body, `"truncated":true`) ||
		!contains(body, `"window_kind_id":"codex/overall/rate_limit/18000"`) ||
		!contains(body, `"observed_at":"2026-07-23T12:00:00Z"`) {
		t.Fatalf("unexpected observation response: %s", body)
	}
}

func TestQuotaObservationsRejectsMissingMalformedAndInvalidRange(t *testing.T) {
	testCases := []struct {
		name     string
		path     string
		provider *quotaProviderStub
	}{
		{
			name:     "missing credential",
			path:     "/api/v1/quota/observations?window_kind_id=kind&start=2026-07-01T00%3A00%3A00Z&end=2026-07-02T00%3A00%3A00Z",
			provider: &quotaProviderStub{},
		},
		{
			name:     "malformed start",
			path:     "/api/v1/quota/observations?auth_index=auth-1&window_kind_id=kind&start=nope&end=2026-07-02T00%3A00%3A00Z",
			provider: &quotaProviderStub{},
		},
		{
			name:     "range over ninety days",
			path:     "/api/v1/quota/observations?auth_index=auth-1&window_kind_id=kind&start=2026-01-01T00%3A00%3A00Z&end=2026-07-02T00%3A00%3A00Z",
			provider: &quotaProviderStub{observationErr: quota.ErrValidation},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: testCase.provider})
			req := httptest.NewRequest(http.MethodGet, testCase.path, nil)
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("expected status 400, got %d body=%s", resp.Code, resp.Body.String())
			}
		})
	}
}

func TestQuotaObservationsRejectsAPIKeyViewer(t *testing.T) {
	sessions := auth.NewSessionManager(time.Hour)
	viewerToken, _, err := sessions.CreateAPIKeyViewer(42)
	if err != nil {
		t.Fatalf("CreateAPIKeyViewer returned error: %v", err)
	}
	config := AuthConfig{Enabled: true, LoginPassword: "secret", SessionTTL: time.Hour}
	provider := &quotaProviderStub{}
	router := NewRouter(nil, nil, nil, nil, config, NewAuthHandler(config, sessions), "", OptionalProviders{Quota: provider})
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/quota/observations?auth_index=auth-1&window_kind_id=kind&start=2026-07-01T00%3A00%3A00Z&end=2026-07-02T00%3A00%3A00Z",
		nil,
	)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: viewerToken})
	resp := httptest.NewRecorder()

	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("expected API key viewer to receive 403, got %d body=%s", resp.Code, resp.Body.String())
	}
	if !provider.observationRequest.Start.IsZero() {
		t.Fatalf("provider must not be called for viewer, got %+v", provider.observationRequest)
	}
}

func TestQuotaCacheAllowsMoreThanRefreshLimit(t *testing.T) {
	provider := &quotaProviderStub{}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})
	authIndexes := make([]string, 21)
	for i := range authIndexes {
		authIndexes[i] = "auth-" + strconv.Itoa(i+1)
	}
	bodyBytes, err := json.Marshal(map[string]any{"auth_indexes": authIndexes})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/quota/cache", strings.NewReader(string(bodyBytes)))

	req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	if len(provider.cacheRequest.AuthIndexes) != 21 {
		t.Fatalf("expected cache request to use all requested auth indexes, got %+v", provider.cacheRequest)
	}
}

func TestQuotaInspectionStatusReturnsSummary(t *testing.T) {
	refreshedAt := time.Date(2026, 6, 3, 10, 30, 0, 0, time.UTC)
	completedAt := time.Date(2026, 6, 3, 10, 31, 0, 0, time.UTC)
	provider := &quotaProviderStub{inspectionStatusResponse: quota.InspectionStatus{
		Total: 3, Cached: 2, Running: true, Normal: 1, Unauthorized401: 1, PaymentRequired402: 1, Unauthorized401402: 2, CompletedAt: &completedAt,
		Results: []quota.InspectionResult{{AuthIndex: "auth-1", Name: "Claude Main", Type: "claude", FileName: apiStringPtr("claude-user.json"), Status: quota.InspectionResultStatusNormal, RefreshedAt: &refreshedAt}},
	}}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/quota/inspection", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	if provider.inspectionStatusCalls != 1 || provider.inspectionStartCalls != 0 {
		t.Fatalf("expected status lookup only, got status=%d start=%d", provider.inspectionStatusCalls, provider.inspectionStartCalls)
	}
	body := resp.Body.String()
	if !contains(body, `"total":3`) || !contains(body, `"cached":2`) || !contains(body, `"unauthorized_401_402":2`) || !contains(body, `"completed_at":"2026-06-03T10:31:00Z"`) || !contains(body, `"auth_index":"auth-1"`) || !contains(body, `"file_name":"claude-user.json"`) || !contains(body, `"refreshed_at":"2026-06-03T10:30:00Z"`) {
		t.Fatalf("unexpected response body: %s", body)
	}
	if contains(body, `"provider"`) {
		t.Fatalf("expected inspection response to use type/name only, got %s", body)
	}
}

func TestQuotaInspectionStartReturnsFreshStatus(t *testing.T) {
	provider := &quotaProviderStub{inspectionStartResponse: quota.InspectionStatus{Total: 2, Cached: 0, Running: true}}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/quota/inspection", nil)

	req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	if provider.inspectionStartCalls != 1 || provider.inspectionStatusCalls != 0 {
		t.Fatalf("expected inspection start only, got start=%d status=%d", provider.inspectionStartCalls, provider.inspectionStatusCalls)
	}
	if body := resp.Body.String(); !contains(body, `"total":2`) || !contains(body, `"cached":0`) || !contains(body, `"running":true`) {
		t.Fatalf("unexpected response body: %s", body)
	}
}

func TestQuotaRefreshCreatesTasksForCurrentPageAuthIndexes(t *testing.T) {
	provider := &quotaProviderStub{refreshResponse: quota.RefreshResponse{
		Tasks:    []quota.RefreshTaskRef{{AuthIndex: "auth-1"}, {AuthIndex: "auth-2"}},
		Accepted: 2,
		Limit:    2,
	}}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/quota/refresh", strings.NewReader(`{"auth_indexes":["auth-1","auth-2"]}`))

	req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	if got := strings.Join(provider.refreshRequest.AuthIndexes, ","); got != "auth-1,auth-2" {
		t.Fatalf("expected auth indexes to be forwarded, got %+v", provider.refreshRequest.AuthIndexes)
	}
	if provider.refreshRequest.Source != quota.RefreshSourceManual {
		t.Fatalf("expected manual refresh source, got %q", provider.refreshRequest.Source)
	}
	body := resp.Body.String()
	if !contains(body, `"tasks"`) || !contains(body, `"authIndex":"auth-1"`) || contains(body, `"taskId"`) || !contains(body, `"accepted":2`) || !contains(body, `"limit":2`) {
		t.Fatalf("unexpected response body: %s", body)
	}
}

func TestQuotaRefreshAllowsCurrentPageSizeWithoutOuterTwentyLimit(t *testing.T) {
	provider := &quotaProviderStub{refreshResponse: quota.RefreshResponse{Accepted: 25, Limit: 25}}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})
	authIndexes := make([]string, 0, 25)
	for i := 0; i < 25; i++ {
		authIndexes = append(authIndexes, `"auth"`)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/quota/refresh", strings.NewReader(`{"auth_indexes":[`+strings.Join(authIndexes, ",")+"]}"))

	req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	if len(provider.refreshRequest.AuthIndexes) != 25 {
		t.Fatalf("expected refresh to forward all current-page auth indexes, got %+v", provider.refreshRequest)
	}
}

func TestQuotaRefreshRejectsEmptyAuthIndexes(t *testing.T) {
	provider := &quotaProviderStub{}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/quota/refresh", strings.NewReader(`{"auth_indexes":[]}`))

	req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", resp.Code, resp.Body.String())
	}
	if provider.refreshRequest.AuthIndexes != nil {
		t.Fatalf("provider should not be called for empty refresh request, got %+v", provider.refreshRequest)
	}
}

func TestQuotaRefreshTaskReturnsCachedQuotaByAuthIndex(t *testing.T) {
	refreshedAt := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	provider := &quotaProviderStub{taskResponse: quota.RefreshTaskResponse{
		AuthIndex:   "auth-1",
		FileName:    apiStringPtr("claude-user.json"),
		Status:      quota.RefreshTaskStatusCompleted,
		RefreshedAt: &refreshedAt,
		Quota:       &quota.CheckResponse{ID: "auth-1", Quota: []quota.QuotaRow{{Key: "rate_limit.primary_window", Label: "5h", PlanType: "pro"}}},
	}}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/quota/refresh/auth-1", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	if provider.taskAuthIndex != "auth-1" {
		t.Fatalf("expected auth_index to be forwarded, got %q", provider.taskAuthIndex)
	}
	body := resp.Body.String()
	if contains(body, `"taskId"`) || contains(body, `"cachedAt"`) || !contains(body, `"file_name":"claude-user.json"`) || !contains(body, `"refreshed_at":"2026-05-26T12:00:00Z"`) || !contains(body, `"status":"completed"`) || !contains(body, `"quota":{"id":"auth-1"`) || !contains(body, `"key":"rate_limit.primary_window"`) || !contains(body, `"planType":"pro"`) {
		t.Fatalf("unexpected response body: %s", body)
	}
}

func TestQuotaRefreshTaskMapsNotFoundTo404(t *testing.T) {
	provider := &quotaProviderStub{taskErr: quota.ErrTaskNotFound}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/quota/refresh/missing-task", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestQuotaDoesNotExposeProviderSpecificEndpoints(t *testing.T) {
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: &quotaProviderStub{}})
	paths := []string{
		"/api/v1/quota/antigravity",
		"/api/v1/quota/codex",
		"/api/v1/quota/gemini-cli",
		"/api/v1/quota/gemini-cli/code-assist",
		"/api/v1/quota/claude",
		"/api/v1/quota/kimi",
	}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != http.StatusNotFound {
			t.Fatalf("expected %s to return 404, got %d", path, resp.Code)
		}
	}
}

func TestQuotaResetReturnsResetResponse(t *testing.T) {
	provider := &quotaProviderStub{resetResponse: quota.ResetResponse{AuthIndex: "codex-auth", Code: "reset", WindowsReset: 2}}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/quota/reset", strings.NewReader(`{"auth_index":"codex-auth"}`))

	req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	if provider.resetRequest.AuthIndex != "codex-auth" {
		t.Fatalf("expected reset request auth_index codex-auth, got %+v", provider.resetRequest)
	}
	body := resp.Body.String()
	if !contains(body, `"authIndex":"codex-auth"`) || !contains(body, `"code":"reset"`) || !contains(body, `"windowsReset":2`) {
		t.Fatalf("unexpected response body: %s", body)
	}
}

func TestQuotaResetRejectsEmptyAuthIndex(t *testing.T) {
	provider := &quotaProviderStub{}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/quota/reset", strings.NewReader(`{"auth_index":"   "}`))

	req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", resp.Code, resp.Body.String())
	}
	if provider.resetRequest.AuthIndex != "" {
		t.Fatalf("provider should not be called for empty auth_index, got %+v", provider.resetRequest)
	}
}

func TestQuotaResetMapsNotFoundTo404(t *testing.T) {
	provider := &quotaProviderStub{resetErr: quota.ErrNotFound}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/quota/reset", strings.NewReader(`{"auth_index":"missing-auth"}`))

	req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestQuotaResetMapsUnsupportedTypeTo400(t *testing.T) {
	provider := &quotaProviderStub{resetErr: quota.ErrUnsupportedType}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/quota/reset", strings.NewReader(`{"auth_index":"claude-auth"}`))

	req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", resp.Code, resp.Body.String())
	}
	if body := resp.Body.String(); !contains(body, `"error":"quota_reset_failed"`) || !contains(body, "quota identity type is unsupported") {
		t.Fatalf("unexpected response body: %s", body)
	}
}

func TestQuotaResetMapsProviderHTTPErrorToTargetStatus(t *testing.T) {
	provider := &quotaProviderStub{resetErr: quota.ProviderHTTPError{StatusCode: 429, Message: "rate limited"}}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/quota/reset", strings.NewReader(`{"auth_index":"codex-auth"}`))

	req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d body=%s", resp.Code, resp.Body.String())
	}
	if body := resp.Body.String(); !contains(body, `"error":"quota_reset_failed"`) || !contains(body, "HTTP 429: rate limited") {
		t.Fatalf("unexpected response body: %s", body)
	}
}

func TestQuotaResetMapsProviderUnauthorizedAwayFromAppAuth(t *testing.T) {
	provider := &quotaProviderStub{resetErr: quota.ProviderHTTPError{StatusCode: http.StatusUnauthorized, Message: "invalid codex token"}}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/quota/reset", strings.NewReader(`{"auth_index":"codex-auth"}`))

	req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadGateway {
		t.Fatalf("expected provider 401 to map to 502, got %d body=%s", resp.Code, resp.Body.String())
	}
	if body := resp.Body.String(); !contains(body, `"error":"quota_reset_failed"`) || !contains(body, "HTTP 401: invalid codex token") {
		t.Fatalf("unexpected response body: %s", body)
	}
}

func TestQuotaResetMapsValidationTo400(t *testing.T) {
	provider := &quotaProviderStub{resetErr: quota.ErrValidation}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/quota/reset", strings.NewReader(`{"auth_index":"codex-auth"}`))

	req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", resp.Code, resp.Body.String())
	}
	if body := resp.Body.String(); !contains(body, "auth_index is required") {
		t.Fatalf("unexpected response body: %s", body)
	}
}

func TestQuotaResetMapsResetInProgressTo409(t *testing.T) {
	provider := &quotaProviderStub{resetErr: quota.ErrResetInProgress}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/quota/reset", strings.NewReader(`{"auth_index":"codex-auth"}`))

	req.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d body=%s", resp.Code, resp.Body.String())
	}
	if body := resp.Body.String(); !contains(body, `"error":"quota_reset_failed"`) || !contains(body, "quota reset already in progress") {
		t.Fatalf("unexpected response body: %s", body)
	}
}
