package utils

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/mattn/go-colorable"
)

var ginLogLock sync.Mutex
var isGinLogInitWorking atomic.Bool

var sysLogLock sync.Mutex
var sysLogInitWorking atomic.Bool

func NewSysLogger(logName string, max int) (logger *SysLogger, err error) {
	if *LogPath != "" {
		if ok := sysLogLock.TryLock(); !ok {
			log.Print("setup log is already working")
			return nil, errors.New("setup log is already working")
		}

		sysLogInitWorking.Store(true)
		defer func() {
			sysLogLock.Unlock()
			sysLogInitWorking.Store(false)
		}()
		if err := os.MkdirAll(*LogPath, 0755); err != nil {
			log.Fatalf("failed to create log dir %q: %v", *LogPath, err)
		}
		path := filepath.Join(*LogPath, fmt.Sprintf("%s-log-%s.log", logName, time.Now().Format("20060102150405")))
		fd, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)

		if err != nil {
			log.Fatal("failed to open log file")
			return nil, err
		}

		cstd := colorable.NewColorableStdout()
		cstdErr := colorable.NewColorableStderr()

		logger = initLogger(logName, max, cstd, fd, false)
		logger.stderr = cstdErr
		return logger, nil

	}
	sysLogInitWorking.Store(false)
	return nil, errors.New("File Path Not Found")
}

var ServerLogger *SysLogger

func NewGinServerLogger(logName string, max int) {
	if *LogPath != "" {
		if ok := ginLogLock.TryLock(); !ok {
			log.Print("setup log is already working")
			return
		}

		isGinLogInitWorking.Store(true)
		defer func() {
			ginLogLock.Unlock()
			isGinLogInitWorking.Store(false)
		}()
		if err := os.MkdirAll(*LogPath, 0755); err != nil {
			log.Fatalf("failed to create log dir %q: %v", *LogPath, err)
		}
		path := filepath.Join(*LogPath, fmt.Sprintf("%s-log-%s.log", logName, time.Now().Format("20060102150405")))
		fd, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)

		if err != nil {
			log.Fatal("failed to open log file")
		}
		cstd := colorable.NewColorableStdout()
		cstdErr := colorable.NewColorableStderr()

		ServerLogger = initLogger(logName, max, cstd, fd, true)
		gin.DefaultWriter = ServerLogger
		gin.DefaultErrorWriter = initLogger(logName, max, cstdErr, fd, true)

	}
	isGinLogInitWorking.Store(false)
}

func initLogger(logName string, max int, console, file io.Writer, forGin bool) *SysLogger {
	return &SysLogger{
		console:    console,
		stderr:     console,
		file:       file,
		loggerName: logName,
		rotateLock: sync.Mutex{},
		strip:      regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`),
		max:        max,
		GinServer:  forGin,
		counter:    0,
	}
}

// log level
const (
	DEBUG   = 0
	INFO    = 1
	WARNING = 2
	ERROR   = 3
)

type SysLogger struct {
	console    io.Writer
	stderr     io.Writer
	file       io.Writer
	loggerName string
	rotateLock sync.Mutex
	strip      *regexp.Regexp
	GinServer  bool
	max        int
	counter    int
}

func (s *SysLogger) Write(p []byte) (n int, err error) {
	return s.writeTo(s.console, p)
}

func (s *SysLogger) writeTo(console io.Writer, p []byte) (n int, err error) {
	if _, err = console.Write(p); err != nil {
		return len(p), err
	}
	clean := s.strip.ReplaceAll(p, []byte(""))
	if _, err = s.file.Write(clean); err != nil {
		return len(p), err
	}
	return len(p), nil
}

type levelWriter struct {
	logger  *SysLogger
	console io.Writer
}

func (w levelWriter) Write(p []byte) (n int, err error) {
	return w.logger.writeTo(w.console, p)
}

func (s *SysLogger) Debug(msg string) {
	if !DebugMode {
		return
	}
	s.helper(DEBUG, msg)
}

func (s *SysLogger) Info(msg string) {
	s.helper(INFO, msg)
}

func (s *SysLogger) Warn(msg string) {
	s.helper(WARNING, msg)
}

func (s *SysLogger) Error(msg string) {
	s.helper(ERROR, msg)
}

// With Formater
func (s *SysLogger) Debugf(msg string, args ...any) {
	if !DebugMode {
		return
	}
	s.helperf(DEBUG, msg, args...)
}

func (s *SysLogger) Infof(msg string, args ...any) {
	s.helperf(INFO, msg, args...)
}

func (s *SysLogger) Warnf(msg string, args ...any) {
	s.helperf(WARNING, msg, args...)
}

func (s *SysLogger) Errorf(msg string, args ...any) {
	s.helperf(ERROR, msg, args...)
}

func (s *SysLogger) Fatal(msg string, args ...any) {
	t := time.Now()
	perfix := fmt.Sprintf("%s[%s] FATAL| at %s, %s", ColorBrightMagenta, s.loggerName, t.Format("2006/01/02-15:04:05"), ColorReset)
	m := perfix + msg
	writer := s.writerForLevel(ERROR)
	_, _ = fmt.Fprintf(writer, m, args...)
	os.Exit(1)
}

func (s *SysLogger) formater(level int) string {
	var levelPerFix string
	t := time.Now()
	switch level {
	case 0:
		levelPerFix = fmt.Sprintf("%s[%s] DEBUG %v| %s", ColorCyan, s.loggerName, t.Format("2006/01/02-15:04:05"), ColorReset)
	case 1:
		levelPerFix = fmt.Sprintf("%s[%s] INFO  %v| %s", ColorGreen, s.loggerName, t.Format("2006/01/02-15:04:05"), ColorReset)
	case 2:
		levelPerFix = fmt.Sprintf("%s[%s] WARN  %v| %s", ColorBrightYellow, s.loggerName, t.Format("2006/01/02-15:04:05"), ColorReset)
	case 3:
		levelPerFix = fmt.Sprintf("%s[%s] ERROR %v| %s", ColorRed, s.loggerName, t.Format("2006/01/02-15:04:05"), ColorReset)
	default:
		levelPerFix = fmt.Sprintf("%s[%s] INFO  %v| %s", ColorBrightGreen, s.loggerName, t.Format("2006/01/02-15:04:05"), ColorReset)
	}

	return levelPerFix
}

func (s *SysLogger) writerForLevel(level int) io.Writer {
	if s.GinServer {
		if level == INFO || level == DEBUG {
			return gin.DefaultWriter
		}
		return gin.DefaultErrorWriter
	}
	if level == INFO || level == DEBUG {
		return levelWriter{logger: s, console: s.console}
	}
	return levelWriter{logger: s, console: s.stderr}
}

func (s *SysLogger) helper(level int, msg string) {
	writer := s.writerForLevel(level)

	levelPerfix := s.formater(level)
	m := levelPerfix + msg
	_, _ = fmt.Fprint(writer, m+"\n")
	s.counter++
	if s.GinServer {
		if s.counter > s.max && !isGinLogInitWorking.Load() {
			s.rebuild()
		}
	} else if s.counter > s.max && !sysLogInitWorking.Load() {
		s.rebuild()
	}
}

func (s *SysLogger) helperf(level int, msg string, args ...any) {
	writer := s.writerForLevel(level)

	levelPerfix := s.formater(level)
	m := levelPerfix + msg
	_, _ = fmt.Fprintf(writer, m, args...)
	fmt.Fprint(writer, "\n")
	s.counter++
	if s.GinServer {
		if s.counter > s.max && !isGinLogInitWorking.Load() {
			s.rebuild()
		}
	} else if s.counter > s.max && !sysLogInitWorking.Load() {
		s.rebuild()
	}

}

func (s *SysLogger) selfRotate(logName string, max int) {
	if ok := s.rotateLock.TryLock(); !ok {
		return
	}
	defer s.rotateLock.Unlock()
	defer sysLogInitWorking.Store(false)
	path := filepath.Join(*LogPath, fmt.Sprintf("%s-log-%s.log", logName, time.Now().Format("20060102150405")))
	fd, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatal("failed to open log file")
	}
	s.file = fd
}

func (s *SysLogger) rebuild() {
	s.counter = 0
	var f func(logName string, max int)
	if s.GinServer {
		isGinLogInitWorking.Store(true)
		f = NewGinServerLogger
	} else {
		sysLogInitWorking.Store(true)
		f = s.selfRotate
	}
	gopool.Go(func() {
		f(s.loggerName, s.max)
	})
}
