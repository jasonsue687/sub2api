package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/gin-gonic/gin"
)

func registerAnthropicMockRoutes(adminGroup *gin.RouterGroup) {
	adminGroup.GET("/anthropic-mock", admin.GetAnthropicMockStatus)
	adminGroup.PUT("/anthropic-mock", admin.UpdateAnthropicMock)
	adminGroup.GET("/anthropic-mock/samples", admin.ListAnthropicMockSamples)
	adminGroup.GET("/anthropic-mock/outbound", admin.ListAnthropicMockOutbound)
	adminGroup.POST("/anthropic-mock/run", admin.RunAnthropicMockTests)
}
