package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicaudit"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// beginAnthropicInbound records the client body before gateway rewrites.
// The returned function is nil when inbound capture is disabled.
func beginAnthropicInbound(c *gin.Context, apiKey *service.APIKey, userID int64, body []byte) func() {
	if c == nil || c.Request == nil || apiKey == nil {
		return nil
	}
	username := ""
	if apiKey.User != nil {
		username = apiKey.User.Username
	}
	endpoint := ""
	if c.Request.URL != nil {
		endpoint = c.Request.URL.Path
	}
	req, done := anthropicaudit.CaptureInbound(c.Request, body, anthropicaudit.InboundCaller{
		UserID: userID, APIKeyID: apiKey.ID, APIKeyName: apiKey.Name, Username: username, Endpoint: endpoint,
	})
	if done == nil {
		return nil
	}
	c.Request = req
	return func() { done(c.Writer.Status()) }
}
