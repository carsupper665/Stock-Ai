package middleware

import (
	"context"
	"server/utils"

	"github.com/gin-gonic/gin"
)

func RequestId() func(c *gin.Context) {
	return func(c *gin.Context) {
		id := utils.GetTimeString() + utils.GetRandomString(6)
		c.Set(utils.RequestIdKey, id)
		ctx := context.WithValue(c.Request.Context(), utils.RequestIdKey, id)
		c.Request = c.Request.WithContext(ctx)
		c.Header(utils.RequestIdKey, id)
		c.Next()
	}
}
