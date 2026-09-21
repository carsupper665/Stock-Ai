//go:build race

package test

import "time"

// -race 行程的 exec 往返路徑明顯較慢，fixture 需要更大的 timeout 預算，
// 否則 version/authentication/invalid-output 子測試會誤判為 PROVIDER_TIMEOUT。
var fixtureTimeout = 15 * time.Second
