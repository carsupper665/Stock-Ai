package main

import (
    "fmt"

    "server/model"
    "server/router"
    "server/service"
    "server/utils"

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
        gin.SetMode(gin.ReleaseMode)
    } else {
        gin.SetMode(gin.DebugMode)
    }

    if err := model.InitDb(); err != nil {
        logger.Fatal("database init error: %s", err)
    }

    app, err := service.NewApp(service.AppConfig{
        DB:            model.DB,
        SessionSecret: utils.SessionSecret,
        FrontendURL:   utils.FrontEndUrl,
    })
    if err != nil {
        logger.Fatal("failed to build app: %s", err)
    }

    engine := router.New(app)
    port := utils.GetEnvString("PORT", "7794")
    logger.Infof("Server running on: %s", port)
    if err := engine.Run(":" + port); err != nil {
        logger.Fatal("failed to start HTTP server: %s", err)
    }
}
