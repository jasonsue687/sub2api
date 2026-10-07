package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type AccountFingerprintHandler struct {
	store service.AccountFingerprintStore
}

func NewAccountFingerprintHandler(store service.AccountFingerprintStore) *AccountFingerprintHandler {
	return &AccountFingerprintHandler{store: store}
}

func (h *AccountFingerprintHandler) List(c *gin.Context) {
	page, size := response.ParsePagination(c)
	source := c.Query("source")
	if source != "" && source != "request" && source != "cache" {
		response.BadRequest(c, "Invalid fingerprint source")
		return
	}
	items, total, err := h.store.List(c.Request.Context(), page, size, fingerprintSearch(c.Query("search")), source)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

func (h *AccountFingerprintHandler) Get(c *gin.Context) {
	id, ok := fingerprintParamID(c)
	if !ok {
		return
	}
	item, err := h.store.Get(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

func (h *AccountFingerprintHandler) Accounts(c *gin.Context) {
	page, size := response.ParsePagination(c)
	items, total, err := h.store.Accounts(c.Request.Context(), fingerprintSearch(c.Query("search")), page, size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

func (h *AccountFingerprintHandler) Binding(c *gin.Context) {
	id, ok := fingerprintParamID(c)
	if !ok {
		return
	}
	item, err := h.store.Binding(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"fingerprint": item, "applied": false})
}

func (h *AccountFingerprintHandler) Bind(c *gin.Context) {
	id, ok := fingerprintParamID(c)
	if !ok {
		return
	}
	var input struct {
		FingerprintID json.RawMessage `json:"fingerprint_id"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || len(input.FingerprintID) == 0 {
		response.BadRequest(c, "fingerprint_id is required (use null to unbind)")
		return
	}
	var fingerprintID *int64
	if !bytes.Equal(bytes.TrimSpace(input.FingerprintID), []byte("null")) {
		var value int64
		if err := json.Unmarshal(input.FingerprintID, &value); err != nil || value <= 0 {
			response.BadRequest(c, "Invalid fingerprint ID")
			return
		}
		fingerprintID = &value
	}
	if err := h.store.Bind(c.Request.Context(), id, fingerprintID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"account_id": id, "fingerprint_id": fingerprintID, "applied": false})
}

func (h *AccountFingerprintHandler) ImportCache(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	result, err := h.store.ImportCache(ctx)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func fingerprintParamID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid ID")
		return 0, false
	}
	return id, true
}

func fingerprintSearch(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}
