package controller

import "server/sandbox"

type BinanceClient = sandbox.BinanceClient

type BinanceAPIError = sandbox.BinanceAPIError

type BookTicker = sandbox.BookTicker

type LatestData = sandbox.LatestData

var ErrRateLimited = sandbox.ErrRateLimited

func NewBinanceClient(baseURL string) *BinanceClient {
	return sandbox.NewBinanceClient(baseURL)
}
