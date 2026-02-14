package utils

import (
	"flag"
	"os"
	"strconv"
)

var (
	LogPath       = flag.String("log-dir", "./logs", "specify the log directory")
	DCWebHookUrl  string // Web Hook Url, Send Message to Discord Chat
	SessionSecret string
	FrontEndUrl   string
)

func getEnv(key string) (string, bool) {
	v := os.Getenv(key)
	if v == "" {
		return v, false
	}
	return v, true
}

func GetEnvString(key, def string) string {
	if v, ok := getEnv(key); ok {
		return v
	}
	return def
}

func GetEnvInt(key string, def int) int {

	if v, ok := getEnv(key); ok {
		if n, err := strconv.Atoi(v); err != nil {
			return def
		} else {
			return n
		}
	}

	return def
}

func GetEnvBool(key string, def bool) bool {
	if v, ok := getEnv(key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
		return def
	}
	return def
}
