package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
)

func registerAccountFingerprintRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	if h.Admin.AccountFingerprint == nil {
		return
	}
	registry := admin.Group("/account-fingerprints")
	registry.GET("", h.Admin.AccountFingerprint.List)
	registry.GET("/accounts", h.Admin.AccountFingerprint.Accounts)
	registry.POST("/import-cache", h.Admin.AccountFingerprint.ImportCache)
	registry.GET("/:id", h.Admin.AccountFingerprint.Get)
	admin.GET("/accounts/:id/fingerprint-binding", h.Admin.AccountFingerprint.Binding)
	admin.PUT("/accounts/:id/fingerprint-binding", h.Admin.AccountFingerprint.Bind)
}
