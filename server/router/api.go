package router

import (
	"time"

	"github.com/gin-gonic/gin"
)

func ApiRouter(router *gin.Engine) {
	api := router.Group("/api")
	v1 := api.Group("/v1")

	{
		v1.GET("/healthz", func(c *gin.Context) {
			c.JSON(200, gin.H{
				"message": "ok",
			})
			return
		})

		v1.GET("/time", func(c *gin.Context) {
			t := time.Now()
			c.JSON(200, gin.H{"time": t.String()})
			return
		}) // server time

		//v1.GET("/exchangeInfo") // 交易對規格、minQty、tickSize、費率等
	}

	mkt := v1.Group("/market")
	{
		mkt.GET("/ticker")      // query: ?symbol=BTCUSDT (或不帶=全市場)
		mkt.GET("/ticker/24hr") // query: ?symbol=BTCUSDT
		mkt.GET("/depth")       // orderbook 深度(可選), query: ?symbol=BTCUSDT&limit=50
		mkt.GET("/trades")      // 最近成交, query: ?symbol=BTCUSDT&limit=200
		mkt.GET("/klines")      // K線, query: ?symbol=BTCUSDT&interval=1m&limit=500
		mkt.GET("/price")       // 最新成交價, query: ?symbol=BTCUSDT
		mkt.GET("/markPrice")   // 永續用(可先回 last), query: ?symbol=BTCUSDT
		mkt.GET("/fundingRate") // 永續用(可先回固定), query: ?symbol=BTCUSDT&limit=100
	}

	// -------------------------
	// 帳戶 / 錢包（Account / Wallet）
	// -------------------------
	acct := v1.Group("/account")
	{
		acct.GET("/balances") // query: ?wallet=spot|futures  回 available/frozen/total
		acct.GET("/ledger")   // query: ?wallet=spot|futures&asset=USDT&limit=200
		acct.GET("/fees")     // maker/taker fee、VIP等級(沙箱可固定)
		acct.GET("/summary")  // 快照：balances + openOrders + positions(若有)

		// 沙箱常用：入金/重置/內部轉帳
		acct.POST("/deposit")  // body: {wallet, asset, amount}
		acct.POST("/withdraw") // (可選) body: {wallet, asset, amount}
		acct.POST("/transfer") // body: {fromWallet, toWallet, asset, amount}
		acct.POST("/reset")    // 清空訂單/成交/倉位/餘額回到初始狀態
	}

	// -------------------------
	// Spot 交易（現貨）
	// -------------------------
	spot := v1.Group("/spot")
	{
		// 下單 / 撤單 / 查單
		spot.POST("/order")        // create order
		spot.DELETE("/order")      // cancel single, query: ?symbol=BTCUSDT&orderId=123
		spot.DELETE("/openOrders") // cancel all open orders, query: ?symbol=BTCUSDT (可選)
		spot.GET("/order")         // get order, query: ?symbol=BTCUSDT&orderId=123
		spot.GET("/openOrders")    // query: ?symbol=BTCUSDT
		spot.GET("/orders")        // 歷史訂單, query: ?symbol=BTCUSDT&status=&limit=
		spot.GET("/myTrades")      // 我的成交, query: ?symbol=BTCUSDT&limit=

		// 風控/限制（Spot 版可簡化）
		spot.GET("/limits") // 最小下單量、最小名目等（可從 exchangeInfo 拆出）
	}

	// -------------------------
	// Futures 永續（第二階段才需要）
	// -------------------------
	fut := v1.Group("/futures")
	{
		// 下單 / 撤單 / 查單
		fut.POST("/order")
		fut.DELETE("/order")   // query: ?symbol=BTCUSDT&orderId=123
		fut.GET("/order")      // query: ?symbol=BTCUSDT&orderId=123
		fut.GET("/openOrders") // query: ?symbol=BTCUSDT
		fut.GET("/orders")     // query: ?symbol=BTCUSDT&status=&limit=
		fut.GET("/myTrades")   // query: ?symbol=BTCUSDT&limit=

		fut.GET("/position")        // 單一, query: ?symbol=BTCUSDT ?symbol=ALL 全部持倉
		fut.POST("/position/close") // body: {symbol, type:market, reduceOnly:true}
		fut.POST("/leverage")       // body: {symbol, leverage}
		fut.POST("/marginType")     // body: {symbol, marginType:isolated|cross}
		fut.GET("/account")         // futures 權益、UPnL、可用保證金、MM/IM 等

	}
}
