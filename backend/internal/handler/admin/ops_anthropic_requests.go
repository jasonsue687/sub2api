package admin

import (
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ListAnthropicRequests is registered exclusively under the admin-authenticated Ops group.
func (h *OpsHandler) ListAnthropicRequests(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	id, err := strconv.ParseInt(c.Query("account_id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "A positive account_id is required")
		return
	}
	start, end, err := parseOpsTimeRange(c, "24h")
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	only := false
	if raw := c.Query("only_mismatch"); raw != "" {
		only, err = strconv.ParseBool(raw)
		if err != nil {
			response.BadRequest(c, "Invalid only_mismatch")
			return
		}
	}
	page, size := response.ParsePagination(c)
	result, err := h.opsService.ListAnthropicRequests(c.Request.Context(), &service.AnthropicRequestFilter{AccountID: id, StartTime: start, EndTime: end, Page: page, PageSize: size, OnlyMismatch: only})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
