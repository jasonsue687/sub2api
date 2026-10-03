package middleware

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicmock"
	"github.com/gin-gonic/gin"
)

// AnthropicInboundCapture queues gateway request bodies for the mock corpus.
// Collection is asynchronous: a full queue is dropped instead of blocking.
func AnthropicInboundCapture() gin.HandlerFunc {
	return func(c *gin.Context) {
		anthropicmock.ObserveInbound(c.Request)
		c.Next()
	}
}
