package logging

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAccessLogRecordsRealStatusCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	l, dir := newTestLogger(t, 1000)

	engine := gin.New()
	engine.Use(RequestID())
	engine.Use(AccessLog(l))
	engine.GET("/teapot", func(c *gin.Context) {
		c.JSON(http.StatusTeapot, gin.H{"ok": false})
	})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/teapot", nil))

	if rec.Code != http.StatusTeapot {
		t.Fatalf("預期狀態碼 418，實際 %d", rec.Code)
	}

	data, err := os.ReadFile(logFiles(t, dir)[0])
	if err != nil {
		t.Fatalf("讀取日誌: %v", err)
	}
	// 舊實作在 c.Next() 之前讀 Status()，永遠記到 200。
	if !strings.Contains(string(data), "418") {
		t.Fatalf("存取日誌沒有記錄真實狀態碼 418，內容: %q", data)
	}
}

func TestRequestIDIsSetInHeaderAndLoggedWithRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	l, dir := newTestLogger(t, 1000)

	engine := gin.New()
	engine.Use(RequestID())
	engine.Use(AccessLog(l))
	engine.GET("/ping", func(c *gin.Context) {
		// handler 內用請求的 context 記錄，識別碼應自動附上。
		l.Info(c.Request.Context(), "handler ran")
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ping", nil))

	id := rec.Header().Get(RequestIDHeader)
	if id == "" {
		t.Fatal("回應沒有帶上請求識別碼 header")
	}

	data, err := os.ReadFile(logFiles(t, dir)[0])
	if err != nil {
		t.Fatalf("讀取日誌: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "handler ran | "+id) {
		t.Fatalf("handler 的日誌沒有附上識別碼 %s，內容: %q", id, content)
	}
	if !strings.Contains(content, "/ping") || !strings.Contains(content, id) {
		t.Fatalf("存取日誌缺少路徑或識別碼，內容: %q", content)
	}
}

func TestRequestIDsAreUnique(t *testing.T) {
	gin.SetMode(gin.TestMode)
	l, _ := newTestLogger(t, 100000)
	l.console, l.stderr = io.Discard, io.Discard

	engine := gin.New()
	engine.Use(RequestID())
	engine.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })

	seen := make(map[string]bool, 200)
	for i := 0; i < 200; i++ {
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ping", nil))
		id := rec.Header().Get(RequestIDHeader)
		if seen[id] {
			t.Fatalf("請求識別碼重複: %s", id)
		}
		seen[id] = true
	}
}
