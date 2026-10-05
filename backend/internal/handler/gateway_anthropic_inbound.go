package handler

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicaudit"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// AnthropicInboundCaptureMiddleware runs after authentication and before model
// routing or JSON normalization. Collection never depends on User-Agent.
func AnthropicInboundCaptureMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !anthropicaudit.InboundEnabled() || c.Request == nil || c.Request.Method != http.MethodPost {
			c.Next()
			return
		}
		switch c.Request.URL.Path {
		case "/v1/messages", "/v1/messages/count_tokens", "/messages/count_tokens", "/antigravity/v1/messages", "/antigravity/v1/messages/count_tokens":
		default:
			c.Next()
			return
		}
		apiKey, ok := middleware.GetAPIKeyFromContext(c)
		if !ok || apiKey == nil {
			c.Next()
			return
		}
		// Decoding compressed requests removes encoding headers from the live
		// request. Keep the received headers separately for the capture.
		original := c.Request.Clone(c.Request.Context())
		body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
		if err != nil {
			status, message := http.StatusBadRequest, "Failed to read request body"
			if maxErr, ok := extractMaxBytesError(err); ok {
				status, message = http.StatusRequestEntityTooLarge, buildBodyTooLargeMessage(maxErr.Limit)
			}
			c.AbortWithStatusJSON(status, gin.H{"type": "error", "error": gin.H{"type": "invalid_request_error", "message": message}})
			return
		}
		c.Request.Body = pkghttputil.NewPrereadBody(body)
		username := ""
		if apiKey.User != nil {
			username = apiKey.User.Username
		}
		captured, done := anthropicaudit.CaptureInbound(original, body, anthropicaudit.InboundCaller{
			UserID: apiKey.UserID, APIKeyID: apiKey.ID, APIKeyName: apiKey.Name, Username: username, Endpoint: original.URL.Path,
		})
		if done != nil {
			c.Request = c.Request.WithContext(captured.Context())
			defer func() { done(c.Writer.Status()) }()
		}
		c.Next()
	}
}
