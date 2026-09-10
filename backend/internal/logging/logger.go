package logging

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/mattn/go-colorable"
	"github.com/muesli/termenv"
)

// 日誌等級。
const (
	DEBUG = iota
	INFO
	WARNING
	ERROR
)

const (
	colorDebug = "6"
	colorInfo  = "2"
	colorWarn  = "11"
	colorError = "1"
	colorFatal = "13"
)

// ansiPattern 用來把顏色碼從寫入檔案的內容中去掉，console 保留顏色。
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// Logger 是一個具名的日誌輸出器，同時寫到 console（帶色）與檔案（去色）。
// 寫入路徑不加鎖：檔案 handle 用 atomic 交換，輪替時不會阻塞任何寫入。
type Logger struct {
	name    string
	dir     string
	max     int
	debug   bool
	console io.Writer
	stderr  io.Writer

	file     atomic.Pointer[os.File]
	rotating atomic.Bool

	// counter 只用來決定何時輪替，刻意不做同步。
	// 併發下少算幾行不影響正確性，換來寫入路徑零成本。
	counter int
}

// New 建立一個具名 logger，日誌檔放在 dir 底下，寫超過 max 行後自動輪替。
func New(name, dir string, max int, debug bool) (*Logger, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("建立日誌目錄 %q 失敗: %w", dir, err)
	}
	f, err := openLogFile(dir, name)
	if err != nil {
		return nil, err
	}

	l := &Logger{
		name:    name,
		dir:     dir,
		max:     max,
		debug:   debug,
		console: colorable.NewColorableStdout(),
		stderr:  colorable.NewColorableStderr(),
	}
	l.file.Store(f)
	return l, nil
}

// closeGrace 是輪替後保留舊檔案 handle 的時間。給正在寫入的 goroutine
// 用完手上的 handle，就不需要為了關檔而在寫入路徑加鎖。
var closeGrace = 500 * time.Millisecond

func openLogFile(dir, name string) (*os.File, error) {
	// 檔名帶到毫秒：只到秒的話，同一秒內連續輪替會開到同一個檔案，等於沒輪替。
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.log", name, time.Now().Format("20060102-150405.000")))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("開啟日誌檔 %q 失敗: %w", path, err)
	}
	return f, nil
}

// Close 關閉目前的日誌檔。
func (l *Logger) Close() error {
	if f := l.file.Load(); f != nil {
		return f.Close()
	}
	return nil
}

// Write 讓 Logger 可以直接當成 io.Writer 使用（例如接給第三方套件）。
func (l *Logger) Write(p []byte) (int, error) {
	l.emit(l.console, p)
	return len(p), nil
}

// emit 把內容同時送到 console 與檔案。檔案 handle 以 atomic 讀取，
// 因此輪替期間的交換不會讓這裡讀到壞掉的指標。
func (l *Logger) emit(console io.Writer, p []byte) {
	_, _ = console.Write(p)
	if f := l.file.Load(); f != nil {
		_, _ = f.Write(ansiPattern.ReplaceAll(p, nil))
	}
}

func (l *Logger) Debug(ctx context.Context, msg string) { l.log(ctx, DEBUG, msg) }
func (l *Logger) Info(ctx context.Context, msg string)  { l.log(ctx, INFO, msg) }
func (l *Logger) Warn(ctx context.Context, msg string)  { l.log(ctx, WARNING, msg) }
func (l *Logger) Error(ctx context.Context, msg string) { l.log(ctx, ERROR, msg) }

func (l *Logger) Debugf(ctx context.Context, format string, args ...any) {
	l.log(ctx, DEBUG, fmt.Sprintf(format, args...))
}

func (l *Logger) Infof(ctx context.Context, format string, args ...any) {
	l.log(ctx, INFO, fmt.Sprintf(format, args...))
}

func (l *Logger) Warnf(ctx context.Context, format string, args ...any) {
	l.log(ctx, WARNING, fmt.Sprintf(format, args...))
}

func (l *Logger) Errorf(ctx context.Context, format string, args ...any) {
	l.log(ctx, ERROR, fmt.Sprintf(format, args...))
}

// Fatalf 輸出訊息後直接結束行程，用於啟動階段的致命錯誤。
func (l *Logger) Fatalf(format string, args ...any) {
	prefix := fmt.Sprintf("[%s] FATAL %s| ", l.name, time.Now().Format("2006/01/02-15:04:05"))
	msg := colorize(prefix, colorFatal) + fmt.Sprintf(format, args...) + "\n"
	l.emit(l.stderr, []byte(msg))
	_ = l.Close()
	os.Exit(1)
}

func (l *Logger) log(ctx context.Context, level int, msg string) {
	if level == DEBUG && !l.debug {
		return
	}
	if id := RequestIDFrom(ctx); id != "" {
		msg = msg + " | " + id
	}

	console := l.console
	if level >= WARNING {
		console = l.stderr
	}
	l.emit(console, []byte(l.prefix(level)+msg+"\n"))
	l.countLine()
}

func (l *Logger) prefix(level int) string {
	var label, color string
	switch level {
	case DEBUG:
		label, color = "DEBUG", colorDebug
	case WARNING:
		label, color = "WARN ", colorWarn
	case ERROR:
		label, color = "ERROR", colorError
	default:
		label, color = "INFO ", colorInfo
	}
	text := fmt.Sprintf("[%s] %s %s| ", l.name, label, time.Now().Format("2006/01/02-15:04:05"))
	return colorize(text, color)
}

// countLine 累計輸出行數，超過上限時在背景輪替日誌檔，不阻塞呼叫端。
func (l *Logger) countLine() {
	l.counter++
	if l.counter <= l.max {
		return
	}
	if !l.rotating.CompareAndSwap(false, true) {
		return
	}
	l.counter = 0
	go l.rotate()
}

func (l *Logger) rotate() {
	f, err := openLogFile(l.dir, l.name)
	if err != nil {
		l.rotating.Store(false)
		l.emit(l.stderr, []byte(fmt.Sprintf("日誌輪替失敗: %v\n", err)))
		return
	}

	old := l.file.Swap(f)
	l.rotating.Store(false)
	if old == nil {
		return
	}
	// 立刻關掉舊 handle 會讓正在寫入的 goroutine 撲空掉行；延後關閉即可，
	// 代價只是短時間多開一個 fd。
	go func() {
		time.Sleep(closeGrace)
		_ = old.Close()
	}()
}

func colorize(text, color string) string {
	return termenv.String(text).Foreground(termenv.ColorProfile().Color(color)).String()
}
