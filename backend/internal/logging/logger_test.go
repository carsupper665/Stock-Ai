package logging

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestLogger 建立一個不輸出到 console 的 logger，避免測試洗版。
func newTestLogger(t *testing.T, max int) (*Logger, string) {
	t.Helper()
	dir := t.TempDir()
	l, err := New("test", dir, max, true)
	if err != nil {
		t.Fatalf("建立 logger: %v", err)
	}
	l.console = io.Discard
	l.stderr = io.Discard
	t.Cleanup(func() { _ = l.Close() })
	return l, dir
}

// logFiles 回傳目錄下所有日誌檔，依檔名排序。
func logFiles(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.log"))
	if err != nil {
		t.Fatalf("列出日誌檔: %v", err)
	}
	return files
}

func countLines(t *testing.T, files []string) int {
	t.Helper()
	total := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("讀取 %s: %v", f, err)
		}
		total += bytes.Count(data, []byte("\n"))
	}
	return total
}

// waitFor 輪詢直到條件成立或逾時，用來等待背景輪替完成。
func waitFor(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待逾時: %s", why)
}

func TestRotationCreatesNewFileAndKeepsWriting(t *testing.T) {
	l, dir := newTestLogger(t, 5)
	ctx := context.Background()

	for i := 0; i < 12; i++ {
		l.Info(ctx, "before rotation")
	}
	waitFor(t, "輪替產生第二個日誌檔", func() bool { return len(logFiles(t, dir)) > 1 })

	// 輪替後仍要能繼續寫，而且寫進最新的檔案。
	l.Info(ctx, "after rotation marker")

	files := logFiles(t, dir)
	newest := files[len(files)-1]
	data, err := os.ReadFile(newest)
	if err != nil {
		t.Fatalf("讀取最新日誌檔: %v", err)
	}
	if !strings.Contains(string(data), "after rotation marker") {
		t.Fatalf("輪替後的訊息沒有寫進最新檔案 %s，內容: %q", newest, data)
	}
}

func TestRotationClosesPreviousFile(t *testing.T) {
	// 縮短寬限期，讓測試不必等半秒。
	original := closeGrace
	closeGrace = 10 * time.Millisecond
	t.Cleanup(func() { closeGrace = original })

	l, _ := newTestLogger(t, 3)
	ctx := context.Background()

	first := l.file.Load()
	for i := 0; i < 10; i++ {
		l.Info(ctx, "trigger rotation")
	}
	waitFor(t, "檔案 handle 被換掉", func() bool { return l.file.Load() != first })

	// 過了寬限期後舊 handle 必須關閉，否則就是 fd 洩漏。
	waitFor(t, "舊的檔案 handle 被關閉", func() bool {
		_, err := first.Write([]byte("should fail\n"))
		return err != nil
	})
}

func TestConcurrentWritesDuringRotationLoseNothing(t *testing.T) {
	l, dir := newTestLogger(t, 20)
	ctx := context.Background()

	const goroutines, perGoroutine = 16, 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				l.Info(ctx, "concurrent line")
			}
		}()
	}
	wg.Wait()

	// 讓最後一次背景輪替完成。
	waitFor(t, "輪替結束", func() bool { return !l.rotating.Load() })

	// 輪替時延後關閉舊 handle，因此併發寫入不該掉任何一行。
	total := goroutines * perGoroutine
	if written := countLines(t, logFiles(t, dir)); written != total {
		t.Fatalf("併發寫入掉行: 寫出 %d，送出 %d", written, total)
	}
	if len(logFiles(t, dir)) < 2 {
		t.Fatal("這個測試需要真的發生輪替，但只產生了一個日誌檔")
	}
}

func TestRequestIDIsAppendedWhenPresent(t *testing.T) {
	l, dir := newTestLogger(t, 1000)

	ctx := context.WithValue(context.Background(), requestIDKey{}, "req-abc123")
	l.Info(ctx, "with request id")

	data, err := os.ReadFile(logFiles(t, dir)[0])
	if err != nil {
		t.Fatalf("讀取日誌: %v", err)
	}
	if !strings.Contains(string(data), "with request id | req-abc123") {
		t.Fatalf("訊息後面沒有附上請求識別碼，內容: %q", data)
	}
}

func TestMissingOrNilContextDoesNotPanic(t *testing.T) {
	l, dir := newTestLogger(t, 1000)

	// 這兩種情況在舊實作是未檢查的型別斷言，會 panic。
	l.Info(context.Background(), "no request id in context")
	l.Info(nil, "nil context") //nolint:staticcheck // 刻意傳 nil 驗證不會 panic

	if got := countLines(t, logFiles(t, dir)); got != 2 {
		t.Fatalf("預期寫入 2 行，實際 %d", got)
	}
}

func TestDebugSuppressedWhenDebugOff(t *testing.T) {
	dir := t.TempDir()
	l, err := New("test", dir, 1000, false)
	if err != nil {
		t.Fatalf("建立 logger: %v", err)
	}
	defer l.Close()
	l.console, l.stderr = io.Discard, io.Discard

	ctx := context.Background()
	l.Debug(ctx, "should be dropped")
	l.Info(ctx, "should be kept")

	data, err := os.ReadFile(logFiles(t, dir)[0])
	if err != nil {
		t.Fatalf("讀取日誌: %v", err)
	}
	if strings.Contains(string(data), "should be dropped") {
		t.Fatal("debug 關閉時仍寫出了 debug 訊息")
	}
	if !strings.Contains(string(data), "should be kept") {
		t.Fatal("info 訊息遺失")
	}
}
