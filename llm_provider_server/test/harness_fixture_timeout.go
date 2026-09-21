//go:build !race

package test

import "time"

// fixtureTimeout 是 harness fixture 子測試的 Provider timeout 預算。
// fixture 立即回應，timeout 只決定「fixture 卡住時多久算失敗」。
var fixtureTimeout = time.Second
