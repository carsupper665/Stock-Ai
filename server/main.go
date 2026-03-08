package main

import (
	"fmt"
	"net/http"

	"server/middleware"
	"server/model"
	"server/router"
	"server/utils"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
)

var logger = utils.SysLog

func main() {
	if err := utils.InitLogger("SYS", 1000); err != nil {
		fmt.Print("Log init Fail")
		return
	}
	utils.NewGinServerLogger("SERVER", 1000)
	logger = utils.SysLog

	if err := utils.LoadEnv(); err != nil {
		logger.Fatal("%s", err)
	}

	logger.Infof("System Version: %s%s%s, Build %s%s%s", utils.ColorBrightCyan, utils.Version, utils.ColorReset, utils.ColorYellow, utils.Build, utils.ColorReset)

	if !utils.DebugMode {
		logger.Infof("%sRunning in Release Mode%s", utils.ColorBrightGreen, utils.ColorReset)
	} else {
		logger.Warnf("Your Server is running in %sDebugMode%s", utils.ColorCyan, utils.ColorReset)
	}

	if err := model.InitDb(); err != nil {
		logger.Fatal("DataBase Init Error: %s", err)
	}

	server := newHTTPServer()

	port := utils.GetEnvString("PORT", "7794")
	logger.Infof("Server running on: %s", port)

	if err := server.Run(":" + port); err != nil {
		logger.Fatal("failed to start HTTP server: " + err.Error())
	}

}

func newHTTPServer() *gin.Engine {
	if utils.DebugMode {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	server := gin.New()
	server.Use(gin.CustomRecovery(func(c *gin.Context, recovered any) {
		if logger != nil {
			logger.Errorf("panic detected: %v", recovered)
		}

		panicMsg := fmt.Sprintf("Panic detected: %v", recovered)
		if err := utils.SendErrorToDc(panicMsg); err != nil && logger != nil {
			logger.Errorf("Failed to send error to Discord: %v", err)
		}

		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"message": fmt.Sprintf("Unknow Error: %v", recovered),
				"type":    "unknow_panic",
			},
		})
	}))

	server.Use(middleware.RequestId())
	middleware.SetUpLogger(server)

	sessionSecret := utils.SessionSecret
	if sessionSecret == "" {
		sessionSecret = "123456789"
	}

	store := cookie.NewStore([]byte(sessionSecret))
	store.Options(sessions.Options{
		Path:     "/",
		MaxAge:   2592000, // 30 days
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteStrictMode,
	})
	server.Use(sessions.Sessions("session", store))

	router.SetRouter(server)
	router.ApiRouter(server)

	return server
}
