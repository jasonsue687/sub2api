package admin

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

type anthropicMockSampleView struct {
	ID          int64             `json:"id"`
	CreatedAt   string            `json:"created_at"`
	DedupeKey   string            `json:"dedupe_key"`
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	ClientLabel string            `json:"client_label"`
	ContentType string            `json:"content_type"`
	Headers     map[string]string `json:"headers"`
	BodyPreview string            `json:"body_preview"`
	BodyBytes   int               `json:"body_bytes"`
	BodySHA256  string            `json:"body_sha256"`
}

type anthropicMockOutboundView struct {
	ID            int64             `json:"id"`
	CreatedAt     string            `json:"created_at"`
	AccountID     int64             `json:"account_id"`
	Method        string            `json:"method"`
	URL           string            `json:"url"`
	Headers       map[string]string `json:"headers"`
	BodyPreview   string            `json:"body_preview"`
	BodyBytes     int               `json:"body_bytes"`
	BodyTruncated bool              `json:"body_truncated"`
	MockReason    string            `json:"mock_reason"`
	StatusCode    int               `json:"status_code"`
}

// GetAnthropicMockStatus returns the switch, capture health, and corpus size.
func GetAnthropicMockStatus(c *gin.Context) {
	snap, err := anthropicmock.CurrentStatus(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "failed to read anthropic mock status")
		return
	}
	response.Success(c, snap)
}

// UpdateAnthropicMock persists the global mock switch.
func UpdateAnthropicMock(c *gin.Context) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Enabled == nil {
		response.BadRequest(c, "enabled is required")
		return
	}
	if err := anthropicmock.SetEnabled(c.Request.Context(), *body.Enabled); err != nil {
		if errors.Is(err, anthropicmock.ErrStoreUnavailable) {
			response.Error(c, http.StatusServiceUnavailable, err.Error())
			return
		}
		response.Error(c, http.StatusInternalServerError, "failed to update anthropic mock switch")
		return
	}
	snap, err := anthropicmock.CurrentStatus(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "failed to read anthropic mock status")
		return
	}
	response.Success(c, snap)
}

// ListAnthropicMockSamples returns deduplicated inbound samples.
func ListAnthropicMockSamples(c *gin.Context) {
	page, pageSize, ok := anthropicMockPage(c)
	if !ok {
		return
	}
	st := anthropicMockStore()
	if st == nil {
		response.Error(c, http.StatusServiceUnavailable, anthropicmock.ErrStoreUnavailable.Error())
		return
	}
	items, total, err := st.ListSamples(c.Request.Context(), (page-1)*pageSize, pageSize)
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "failed to list anthropic mock samples")
		return
	}
	views := make([]anthropicMockSampleView, 0, len(items))
	for _, item := range items {
		views = append(views, anthropicMockSampleView{
			ID: item.ID, CreatedAt: item.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			DedupeKey: item.DedupeKey, Method: item.Method, Path: item.Path,
			ClientLabel: item.ClientLabel, ContentType: item.ContentType, Headers: item.Headers,
			BodyPreview: anthropicmock.Preview(item.Body, 2000), BodyBytes: len(item.Body), BodySHA256: item.BodySHA256,
		})
	}
	response.Success(c, response.PaginatedData{Items: views, Total: total, Page: page, PageSize: pageSize, Pages: anthropicMockPages(total, pageSize)})
}

// ListAnthropicMockOutbound returns intercepted outbound Anthropic requests.
func ListAnthropicMockOutbound(c *gin.Context) {
	page, pageSize, ok := anthropicMockPage(c)
	if !ok {
		return
	}
	st := anthropicMockStore()
	if st == nil {
		response.Error(c, http.StatusServiceUnavailable, anthropicmock.ErrStoreUnavailable.Error())
		return
	}
	items, total, err := st.ListOutbound(c.Request.Context(), (page-1)*pageSize, pageSize)
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "failed to list anthropic mock outbound records")
		return
	}
	views := make([]anthropicMockOutboundView, 0, len(items))
	for _, item := range items {
		views = append(views, anthropicMockOutboundView{
			ID: item.ID, CreatedAt: item.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			AccountID: item.AccountID, Method: item.Method, URL: item.URL, Headers: item.Headers,
			BodyPreview: anthropicmock.Preview(item.Body, 2000), BodyBytes: len(item.Body),
			BodyTruncated: item.BodyTruncated, MockReason: item.MockReason, StatusCode: item.StatusCode,
		})
	}
	response.Success(c, response.PaginatedData{Items: views, Total: total, Page: page, PageSize: pageSize, Pages: anthropicMockPages(total, pageSize)})
}

// RunAnthropicMockTests replays the corpus in-process. The run is refused unless
// mock interception is proven before the first sample.
func RunAnthropicMockTests(c *gin.Context) {
	var body struct {
		APIKey string `json:"api_key"`
		Limit  int    `json:"limit"`
	}
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&body); err != nil {
			response.BadRequest(c, "invalid run request")
			return
		}
	}
	report, err := anthropicmock.Run(c.Request.Context(), anthropicmock.RunOptions{APIKey: body.APIKey, Limit: body.Limit})
	if err != nil {
		switch {
		case errors.Is(err, anthropicmock.ErrMockNotGuaranteed), errors.Is(err, anthropicmock.ErrRunBusy):
			response.Error(c, http.StatusConflict, err.Error())
		default:
			response.Error(c, http.StatusServiceUnavailable, "failed to run anthropic mock tests")
		}
		return
	}
	response.Success(c, report)
}

func anthropicMockStore() anthropicmock.Store {
	// The store is installed at startup. Tests replace it through SetStore.
	// Reading it here keeps the handler out of the repository package.
	return anthropicmock.CurrentStore()
}

func anthropicMockPage(c *gin.Context) (int, int, bool) {
	page := 1
	pageSize := 20
	if raw := c.Query("page"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			response.BadRequest(c, "invalid page")
			return 0, 0, false
		}
		page = n
	}
	if raw := c.Query("page_size"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			response.BadRequest(c, "invalid page_size")
			return 0, 0, false
		}
		pageSize = n
	}
	return page, pageSize, true
}

func anthropicMockPages(total int64, pageSize int) int {
	if pageSize <= 0 || total <= 0 {
		return 0
	}
	return int((total + int64(pageSize) - 1) / int64(pageSize))
}
