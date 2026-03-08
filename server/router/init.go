package router

import (
	"fmt"
	"net/http"
	"server/middleware"
	"server/utils"
	"strings"

	"github.com/gin-gonic/gin"
)

func SetRouter(router *gin.Engine) {
	router.Use(middleware.CORS())

	frontendBaseUrl := utils.FrontEndUrl
	if frontendBaseUrl == "" {
		frontendBaseUrl = "http://localhost:3000"
	}

	frontendBaseUrl = strings.TrimSuffix(frontendBaseUrl, "/")
	router.NoRoute(func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, fmt.Sprintf("%s%s", frontendBaseUrl, c.Request.RequestURI))
	})

}
