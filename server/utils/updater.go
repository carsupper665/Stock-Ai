package utils

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"
)

// TODO ReFactor Updater

const (
	tmpName    = "main_new.exe"
	curr       = "main.exe"
	backup     = "main.exe.bk"
	logName    = "update.log"
	checkerLog = "update_checker.log"
	repo       = "carsupper665/mc-server-backend"
)

var (
	ErrAlreadyLatest  = errors.New("already updated")
	ErrUpdateDisabled = errors.New("auto update is disabled")
	ErrAlreadyStarted = errors.New("update checker already started")
	ErrHashMismatch   = errors.New("downloaded file hash mismatch")
)

type status struct {
	running bool
	mu      sync.RWMutex
}

func (s *status) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

func (s *status) SetRunning(r bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = r
}

var UpdaterStatus = &status{
	running: false,
	mu:      sync.RWMutex{},
}

type release struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name               string `json:"name"`
		Digest             string `json:"digest"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

type UpdateLogger struct {
	console io.Writer
	file    io.Writer
	strip   *regexp.Regexp
}

func (u *UpdateLogger) Write(p []byte) (n int, err error) {
	if _, err = u.console.Write(p); err != nil {
		return len(p), err
	}
	clean := u.strip.ReplaceAll(p, []byte(""))
	if _, err = u.file.Write(clean); err != nil {
		return len(p), err
	}
	return len(p), nil
}

func (u *UpdateLogger) Info(msg string) {
	_, _ = fmt.Fprintf(u, "%s%s|%s\n", ColorBrightGreen+"[UPDT][INFO]", ColorReset, msg)
}

func (u *UpdateLogger) Error(msg string) {
	_, _ = fmt.Fprintf(u, "%s%s|%s\n", ColorRed+"[UPDT][ERROR]", ColorReset, msg)
}

func (u *UpdateLogger) Close() error {
	if fileCloser, ok := u.file.(io.Closer); ok {
		return fileCloser.Close()
	}
	return nil
}

func newLogger(console, file io.Writer) *UpdateLogger {
	return &UpdateLogger{
		console: console,
		file:    file,
		strip:   regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`),
	}
}

func getLogger(logFile string) *UpdateLogger {
	if *LogPath != "" {
		logFilePath := fmt.Sprintf("%s/%s", *LogPath, logFile)
		file, err := os.OpenFile(logFilePath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatal("failed to open log file:", err)
		}
		return newLogger(os.Stdout, file)
	}
	return nil
}

// fetchLatestRelease 獲取最新版本資訊
func fetchLatestRelease(logger *UpdateLogger) (*release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		logger.Error("failed to fetch release info: " + err.Error())
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("unexpected status: %s", resp.Status)
		logger.Error(err.Error())
		return nil, err
	}

	var r release
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		logger.Error("failed to decode response: " + err.Error())
		return nil, err
	}

	return &r, nil
}

func downloadAndApplyUpdate(url, expectedHash string, logger *UpdateLogger) error {
	logger.Info("Downloading update...")

	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		logger.Error("failed to download: " + err.Error())
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("unexpected status: %s", resp.Status)
		logger.Error(err.Error())
		return err
	}

	tmpFile, err := os.Create(tmpName)
	if err != nil {
		logger.Error("failed to create temp file: " + err.Error())
		return err
	}

	var downloadErr error
	defer func() {
		tmpFile.Close()
		if downloadErr != nil {
			os.Remove(tmpName)
		}
	}()

	hasher := sha256.New()
	writer := io.MultiWriter(tmpFile, hasher)

	written, err := io.Copy(writer, resp.Body)
	if err != nil {
		downloadErr = err
		logger.Error("failed to save update: " + err.Error())
		return err
	}

	logger.Info(fmt.Sprintf("Downloaded %d bytes", written))

	// 同步到磁碟
	if err := tmpFile.Sync(); err != nil {
		downloadErr = err
		logger.Error("failed to sync: " + err.Error())
		return err
	}

	// 驗證 hash
	hashSum := fmt.Sprintf("%x", hasher.Sum(nil))
	if hashSum != expectedHash {
		downloadErr = ErrHashMismatch
		logger.Error(fmt.Sprintf("hash mismatch: expected %s got %s", expectedHash, hashSum))
		return ErrHashMismatch
	}

	logger.Info("Hash verified successfully")

	// 備份舊版本
	if _, err := os.Stat(curr); err == nil {
		logger.Info("Backing up old executable")
		os.Remove(backup)
		if err := os.Rename(curr, backup); err != nil {
			logger.Error("failed to backup: " + err.Error())
			return err
		}
	}

	// 關閉檔案
	if err := tmpFile.Close(); err != nil {
		logger.Error("failed to close temp file: " + err.Error())
		return err
	}

	// 替換執行檔
	logger.Info("Replacing executable")
	if err := os.Rename(tmpName, curr); err != nil {
		logger.Error("failed to replace exe: " + err.Error())
		return err
	}

	return nil
}

// func StartUpdateChecker(interval time.Duration, onUpdateSuccess func()) {
// 	go updateChecker(interval, onUpdateSuccess)
// }

// updateChecker 背景更新檢查器

// // TODO:Remake Udater
// func updateChecker(interval time.Duration, onUpdateSuccess func()) {
// 	logger := getLogger(checkerLog)
// 	if logger == nil {
// 		SysError("failed to start update checker logger")
// 		return
// 	}
// 	defer logger.Close()

// 	logger.Info(fmt.Sprintf("Update checker started, interval: %s", interval))

// 	ticker := time.NewTicker(interval)
// 	defer ticker.Stop()

// 	for {
// 		select {
// 		case <-ticker.C:
// 			logger.Info("Running scheduled update check...")

// 			if err := performUpdate(logger); err != nil {
// 				if errors.Is(err, ErrAlreadyLatest) {
// 					logger.Info("Already at latest version")
// 				} else if errors.Is(err, ErrUpdateDisabled) {
// 					logger.Info("Auto update is disabled")
// 				} else {
// 					logger.Error("Update check failed: " + err.Error())
// 				}
// 			} else {
// 				// 更新成功
// 				logger.Info("Update successful! Triggering restart...")
// 				if onUpdateSuccess != nil {
// 					onUpdateSuccess()
// 				}
// 				return
// 			}
// 		}
// 	}
// }
