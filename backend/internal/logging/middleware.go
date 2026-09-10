package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/gin-gonic/gin"
)

// RequestIDHeader 同時作為 response header 名稱與 context 中的 key。
const RequestIDHeader = "X-Request-Id"

type requestIDKey struct{}

// RequestID 為每個請求產生識別碼，寫入 context 與 response header。
// 之後所有帶著這個 context 的日誌都會自動附上這組識別碼。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := newRequestID()
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestIDKey{}, id))
		c.Header(RequestIDHeader, id)
		c.Next()
	}
}

// RequestIDFrom 取出 context 中的請求識別碼，沒有就回傳空字串。
func RequestIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func newRequestID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().Format("20060102150405.000000")
	}
	return time.Now().Format("20060102150405") + "-" + hex.EncodeToString(b[:])
}

// AccessLog 記錄每個請求的結果。狀態碼與耗時都在 c.Next() 之後才讀，
// 確保拿到的是真實回應而不是預設值。
func AccessLog(l *Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		if raw := c.Request.URL.RawQuery; raw != "" {
			path = path + "?" + raw
		}

		c.Next()

		l.Infof(c.Request.Context(), "%-15s | %-6s %3d | %-40s | %s",
			c.ClientIP(),
			c.Request.Method,
			c.Writer.Status(),
			path,
			formatLatency(time.Since(start)),
		)
	}
}

// formatLatency 顯示請求耗時。Windows 上 Go 的時鐘粒度約 0.5ms，
// 快速請求會量到 0，直接印 "0s" 看起來像壞掉，改成標示下界。
func formatLatency(d time.Duration) string {
	if d <= 0 {
		return "<1ms"
	}
	return d.Round(time.Microsecond).String()
}
