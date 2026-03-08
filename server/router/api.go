package router

import (
	"time"

	"github.com/gin-gonic/gin"
)

func ApiRouter(router *gin.Engine) {
	api := router.Group("/api")
	v1 := api.Group("/v1")

	v1.GET("/healthz", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "ok",
		})
	})

	v1.GET("/time", func(c *gin.Context) {
		t := time.Now()
		c.JSON(200, gin.H{"time": t.String()})
	})

	// The rest of the sandbox API surface is intentionally deferred to later tasks.
	// Planned routes:
	// - /market/*
	// - /account/*
	// - /spot/*
	// - /futures/*
}
