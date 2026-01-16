package middleware

import (
	"fmt"
	"server/utils"

	"github.com/gin-gonic/gin"
)

func SetUpLogger(server *gin.Engine) {
	server.Use(gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		var requestID string
		if param.Keys != nil {
			requestID = param.Keys[utils.RequestIdKey].(string)
		}
		return fmt.Sprintf("%s | %s | %s %3d | %-50s | %s | %s\n",
			param.TimeStamp.Format("2006/01/02-15:04:05"),
			param.ClientIP,
			param.Method,
			param.StatusCode,
			param.Path,
			param.Latency,
			requestID,
		)
	}))
}
