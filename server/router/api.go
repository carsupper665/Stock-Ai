package router

import "github.com/gin-gonic/gin"

func ApiRouter(router *gin.Engine) {
	apir := router.Group("/api")
	v1r := apir.Group("/v1")
	{
		v1r.GET("/order/buy/:stock_id")
		v1r.GET("/order/sale/:stock_id")

		v1r.GET("/balance") // 餘額

		v1r.GET("/profit") // 損益

	}
}
