package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *OpsHandler) ListAnthropicSessions(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	start, end, err := parseOpsTimeRange(c, "24h")
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	accountID, _ := strconv.ParseInt(c.Query("account_id"), 10, 64)
	onlyMulti, err := optionalBoolQuery(c, "only_multi")
	if err != nil {
		response.BadRequest(c, "Invalid only_multi")
		return
	}
	onlyFailed, err := optionalBoolQuery(c, "only_failed")
	if err != nil {
		response.BadRequest(c, "Invalid only_failed")
		return
	}
	onlyMismatch, err := optionalBoolQuery(c, "only_mismatch")
	if err != nil {
		response.BadRequest(c, "Invalid only_mismatch")
		return
	}
	page, size := response.ParsePagination(c)
	result, err := h.opsService.ListAnthropicSessions(c.Request.Context(), &service.AnthropicSessionFilter{
		AccountID: accountID, StartTime: start, EndTime: end, Page: page, PageSize: size,
		OnlyMulti: onlyMulti, OnlyFailed: onlyFailed, OnlyMismatch: onlyMismatch,
		Model: strings.TrimSpace(c.Query("model")), Query: c.Query("q"),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *OpsHandler) GetAnthropicSession(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	detail, err := h.opsService.GetAnthropicSession(c.Request.Context(), c.Query("client_request_id"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, detail)
}

func optionalBoolQuery(c *gin.Context, name string) (bool, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return false, nil
	}
	return strconv.ParseBool(raw)
}
