package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"cpa-usage-keeper/internal/quota"
	"cpa-usage-keeper/internal/timeutil"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

type quotaRequest struct {
	AuthIndexes []string `json:"auth_indexes"`
}

const quotaResetErrorFailed = "quota_reset_failed"
const quotaResetCreditsErrorFailed = "quota_reset_credits_failed"

func registerQuotaRoutes(router gin.IRoutes, provider QuotaProvider) {
	router.POST("/quota/capacity", func(c *gin.Context) {
		if provider == nil {
			writeInternalError(c, "quota provider is not configured", nil)
			return
		}
		var request quotaRequest
		if err := c.ShouldBindJSON(&request); err != nil || len(request.AuthIndexes) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_indexes are required"})
			return
		}
		response, err := provider.GetCapacity(c.Request.Context(), quota.CapacityRequest{
			AuthIndexes: request.AuthIndexes,
		})
		if err != nil {
			switch {
			case errors.Is(err, quota.ErrValidation):
				c.JSON(http.StatusBadRequest, gin.H{"error": "auth_indexes are required"})
			default:
				writeInternalError(c, "quota capacity lookup failed", err)
			}
			return
		}
		c.JSON(http.StatusOK, response)
	})

	router.GET("/quota/capacity/detail", func(c *gin.Context) {
		if provider == nil {
			writeInternalError(c, "quota provider is not configured", nil)
			return
		}
		authIndex := strings.TrimSpace(c.Query("auth_index"))
		windowKindID := strings.TrimSpace(c.Query("window_kind_id"))
		var epochResetAt *time.Time
		if rawEpochResetAt := strings.TrimSpace(c.Query("epoch_reset_at")); rawEpochResetAt != "" {
			parsed, err := timeutil.ParseStorageTime(rawEpochResetAt)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "capacity detail parameters are invalid"})
				return
			}
			epochResetAt = &parsed
		}
		if authIndex == "" || windowKindID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "capacity detail parameters are invalid"})
			return
		}
		response, err := provider.GetCapacityDetail(c.Request.Context(), quota.CapacityDetailRequest{
			AuthIndex:    authIndex,
			WindowKindID: windowKindID,
			EpochResetAt: epochResetAt,
		})
		if err != nil {
			switch {
			case errors.Is(err, quota.ErrValidation):
				c.JSON(http.StatusBadRequest, gin.H{"error": "capacity detail parameters are invalid"})
			case errors.Is(err, quota.ErrNotFound):
				c.JSON(http.StatusNotFound, gin.H{"error": "quota capacity not found"})
			default:
				writeInternalError(c, "quota capacity detail lookup failed", err)
			}
			return
		}
		c.JSON(http.StatusOK, response)
	})

	router.GET("/quota/observations", func(c *gin.Context) {
		if provider == nil {
			writeInternalError(c, "quota provider is not configured", nil)
			return
		}
		authIndex := strings.TrimSpace(c.Query("auth_index"))
		windowKindID := strings.TrimSpace(c.Query("window_kind_id"))
		start, startErr := timeutil.ParseStorageTime(c.Query("start"))
		end, endErr := timeutil.ParseStorageTime(c.Query("end"))
		if authIndex == "" || windowKindID == "" || startErr != nil || endErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index, window_kind_id, start, and end are required"})
			return
		}
		response, err := provider.ListObservations(c.Request.Context(), quota.ObservationSeriesRequest{
			AuthIndex:    authIndex,
			WindowKindID: windowKindID,
			Start:        start,
			End:          end,
		})
		if err != nil {
			switch {
			case errors.Is(err, quota.ErrValidation):
				c.JSON(http.StatusBadRequest, gin.H{"error": "quota observation range is invalid"})
			case errors.Is(err, quota.ErrNotFound):
				c.JSON(http.StatusNotFound, gin.H{"error": "quota credential not found"})
			default:
				writeInternalError(c, "quota observations lookup failed", err)
			}
			return
		}
		c.JSON(http.StatusOK, response)
	})

	router.GET("/quota/auto-refresh/settings", func(c *gin.Context) {
		if provider == nil {
			writeInternalError(c, "quota provider is not configured", nil)
			return
		}

		response, err := provider.GetAutoRefreshSettings(c.Request.Context())
		if err != nil {
			switch {
			case errors.Is(err, quota.ErrValidation):
				c.JSON(http.StatusBadRequest, gin.H{"error": "quota auto refresh settings are invalid"})
			default:
				writeInternalError(c, "quota auto refresh settings lookup failed", err)
			}
			return
		}

		c.JSON(http.StatusOK, response)
	})

	router.PUT("/quota/auto-refresh/settings", func(c *gin.Context) {
		if provider == nil {
			writeInternalError(c, "quota provider is not configured", nil)
			return
		}

		var request quota.AutoRefreshSettings
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "quota auto refresh settings are required"})
			return
		}

		response, err := provider.UpdateAutoRefreshSettings(c.Request.Context(), request)
		if err != nil {
			switch {
			case errors.Is(err, quota.ErrValidation):
				c.JSON(http.StatusBadRequest, gin.H{"error": "quota auto refresh settings are invalid"})
			default:
				writeInternalError(c, "quota auto refresh settings update failed", err)
			}
			return
		}

		c.JSON(http.StatusOK, response)
	})

	router.POST("/quota/cache", func(c *gin.Context) {
		if provider == nil {
			writeInternalError(c, "quota provider is not configured", nil)
			return
		}

		// 缓存读取只校验查询列表；列表返回多少 auth_index，就按相同数量读取缓存。
		var request quotaRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_indexes are required"})
			return
		}
		if len(request.AuthIndexes) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_indexes are required"})
			return
		}

		response, err := provider.GetCachedQuota(c.Request.Context(), quota.CacheRequest{AuthIndexes: request.AuthIndexes})
		if err != nil {
			switch {
			case errors.Is(err, quota.ErrValidation):
				c.JSON(http.StatusBadRequest, gin.H{"error": "auth_indexes are required"})
			default:
				writeInternalError(c, "quota cache lookup failed", err)
			}
			return
		}

		c.JSON(http.StatusOK, response)
	})

	router.GET("/quota/inspection", func(c *gin.Context) {
		if provider == nil {
			writeInternalError(c, "quota provider is not configured", nil)
			return
		}

		response, err := provider.GetInspectionStatus(c.Request.Context())
		if err != nil {
			writeInternalError(c, "quota inspection status lookup failed", err)
			return
		}

		c.JSON(http.StatusOK, response)
	})

	router.POST("/quota/inspection", func(c *gin.Context) {
		if provider == nil {
			writeInternalError(c, "quota provider is not configured", nil)
			return
		}

		response, err := provider.StartInspection(c.Request.Context())
		if err != nil {
			writeInternalError(c, "quota inspection start failed", err)
			return
		}

		c.JSON(http.StatusOK, response)
	})

	router.POST("/quota/refresh", func(c *gin.Context) {
		if provider == nil {
			writeInternalError(c, "quota provider is not configured", nil)
			return
		}

		var request quotaRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_indexes are required"})
			return
		}
		if len(request.AuthIndexes) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_indexes are required"})
			return
		}

		response, err := provider.Refresh(c.Request.Context(), quota.RefreshRequest{
			AuthIndexes: request.AuthIndexes,
			Source:      quota.RefreshSourceManual,
		})
		if err != nil {
			switch {
			case errors.Is(err, quota.ErrValidation):
				c.JSON(http.StatusBadRequest, gin.H{"error": "auth_indexes are required"})
			default:
				writeInternalError(c, "quota refresh failed", err)
			}
			return
		}

		c.JSON(http.StatusOK, response)
	})

	router.GET("/quota/refresh/:auth_index", func(c *gin.Context) {
		if provider == nil {
			writeInternalError(c, "quota provider is not configured", nil)
			return
		}
		authIndex := strings.TrimSpace(c.Param("auth_index"))
		if authIndex == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
			return
		}

		// 前端轮询直接以 auth_index 查询任务状态，避免维护额外 taskId 映射。
		response, err := provider.GetRefreshTaskByAuthIndex(c.Request.Context(), authIndex)
		if err != nil {
			switch {
			case errors.Is(err, quota.ErrTaskNotFound):
				c.JSON(http.StatusNotFound, gin.H{"error": "quota refresh task not found"})
			default:
				writeInternalError(c, "quota refresh task lookup failed", err)
			}
			return
		}

		c.JSON(http.StatusOK, response)
	})
	router.GET("/quota/reset-credits/:auth_index", func(c *gin.Context) {
		if provider == nil {
			writeInternalError(c, "quota provider is not configured", nil)
			return
		}
		authIndex := strings.TrimSpace(c.Param("auth_index"))
		if authIndex == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
			return
		}
		response, err := provider.GetResetCredits(c.Request.Context(), quota.ResetCreditsRequest{AuthIndex: authIndex})
		if err != nil {
			writeQuotaResetCreditsError(c, quotaProviderErrorStatus(err), err)
			return
		}
		c.JSON(http.StatusOK, response)
	})
	router.POST("/quota/reset", func(c *gin.Context) {
		if provider == nil {
			writeInternalError(c, "quota provider is not configured", nil)
			return
		}

		var request struct {
			AuthIndex string `json:"auth_index"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
			return
		}
		authIndex := strings.TrimSpace(request.AuthIndex)
		if authIndex == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
			return
		}

		response, err := provider.Reset(c.Request.Context(), quota.ResetRequest{AuthIndex: authIndex})
		if err != nil {
			switch {
			case errors.Is(err, quota.ErrValidation):
				c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
			case errors.Is(err, quota.ErrNotFound):
				writeQuotaResetError(c, http.StatusNotFound, err)
			case errors.Is(err, quota.ErrUnsupportedType):
				writeQuotaResetError(c, http.StatusBadRequest, err)
			case errors.Is(err, quota.ErrResetInProgress):
				writeQuotaResetError(c, http.StatusConflict, err)
			default:
				var httpErr quota.ProviderHTTPError
				if errors.As(err, &httpErr) && httpErr.StatusCode >= 100 && httpErr.StatusCode <= 599 {
					statusCode := httpErr.StatusCode
					if statusCode == http.StatusUnauthorized {
						// 这里的 401 来自 Codex 官方接口，不代表 dashboard 登录态失效，前端应按 reset 失败提示处理。
						statusCode = http.StatusBadGateway
					}
					writeQuotaResetError(c, statusCode, err)
					return
				}
				logrus.WithError(err).Error("quota reset failed")
				writeQuotaResetError(c, http.StatusInternalServerError, err)
			}
			return
		}

		c.JSON(http.StatusOK, response)
	})

}

func quotaProviderErrorStatus(err error) int {
	switch {
	case errors.Is(err, quota.ErrValidation), errors.Is(err, quota.ErrUnsupportedType):
		return http.StatusBadRequest
	case errors.Is(err, quota.ErrNotFound):
		return http.StatusNotFound
	}
	var httpErr quota.ProviderHTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode >= 100 && httpErr.StatusCode <= 599 {
		if httpErr.StatusCode == http.StatusUnauthorized {
			return http.StatusBadGateway
		}
		return httpErr.StatusCode
	}
	return http.StatusInternalServerError
}

func writeQuotaResetCreditsError(c *gin.Context, statusCode int, err error) {
	payload := gin.H{"error": quotaResetCreditsErrorFailed}
	if err != nil {
		payload["detail"] = err.Error()
	}
	c.JSON(statusCode, payload)
}

func writeQuotaResetError(c *gin.Context, statusCode int, err error) {
	payload := gin.H{"error": quotaResetErrorFailed}
	if err != nil {
		// detail 仅用于浏览器 Network/F12 排查，不作为前端展示文案。
		payload["detail"] = err.Error()
	}
	c.JSON(statusCode, payload)
}
