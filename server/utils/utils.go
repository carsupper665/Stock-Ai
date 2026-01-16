package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func SendErrorToDc(msg string) error {
	url := DCWebHookUrl
	if url == "" {
		return fmt.Errorf("Discord webhook URL is not set")
	}

	payload := map[string]string{
		"content":  msg,
		"username": SysName,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webhook send failed: status %d, body: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func GetRandomString(length int) string {
	key := make([]byte, length)
	for i := 0; i < length; i++ {
		key[i] = keyChars[rand.Intn(len(keyChars))]
	}
	return string(key)
}

func GetRandomIntString(length int) string {
	key := make([]byte, length)
	for i := 0; i < length; i++ {
		key[i] = NumberChars[rand.Intn(10)] // 只使用數字
	}
	return string(key)
}

func GetTimeString() string {
	now := time.Now()
	return fmt.Sprintf("%s%d", now.Format("20060102150405"), now.UnixNano()%1e9)
}

func DownloadFile(dest, url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("http get %s error: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("bad status downloading %s: %s", url, resp.Status)
	}

	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create file %s error: %w", dest, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("writing to %s error: %w", dest, err)
	}
	return nil
}

func Copy(src, dst string) error {
	err := os.MkdirAll(dst, os.ModePerm) //0777 = os.ModePerm
	if err != nil {
		return err
	}

	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		targetPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		} else {
			return copyFile(path, targetPath)
		}
	})

}

func copyFile(src, dst string) error {
	file, err := os.Open(src)

	if err != nil {
		return err
	}
	defer file.Close()

	dstFile, err := os.Create(dst)

	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, file)

	if err != nil {
		return err
	}
	// 複製檔案權限
	info, err := os.Stat(src)
	if err == nil {
		err = os.Chmod(dst, info.Mode())
	}
	return err
}
