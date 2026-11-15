package middleware

import (
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func ConversationContext() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.PrepareConversationContext(c); err != nil {
			service.SetModelRouteOutcome(c, service.ModelRouteOutcomeClientRejected)
			c.AbortWithStatusJSON(err.StatusCode, gin.H{"error": err.ToOpenAIError()})
			return
		}
		c.Next()
	}
}
