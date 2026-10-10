package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func secret(name, logicalName string) (string, error) {
	environment := os.Getenv("STOCK_AI_ENVIRONMENT")
	direct, directSet := os.LookupEnv(name)
	fileName, fileSet := os.LookupEnv(name + "_FILE")
	if directSet && fileSet {
		return "", fmt.Errorf("%s and %s_FILE cannot both be set", name, name)
	}
	production := environment == "Test" || environment == "Release"
	if environment != "" && !production {
		return "", errors.New("STOCK_AI_ENVIRONMENT must be exactly Test or Release")
	}
	if production && !fileSet {
		return "", fmt.Errorf("%s_FILE is required in %s", name, environment)
	}
	if !fileSet {
		return direct, nil
	}
	namespace := "stock-ai-" + strings.ToLower(environment)
	if production && filepath.Base(fileName) != namespace+"_"+logicalName {
		return "", fmt.Errorf("%s_FILE is outside the %s secret namespace", name, namespace)
	}
	info, err := os.Lstat(fileName)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s_FILE must name a readable regular file", name)
	}
	raw, err := os.ReadFile(fileName)
	if err != nil {
		return "", fmt.Errorf("%s_FILE is unreadable", name)
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", fmt.Errorf("%s_FILE is empty", name)
	}
	if production {
		if err := validateMountedSecret(value, strings.HasSuffix(logicalName, "_key")); err != nil {
			return "", fmt.Errorf("%s_FILE is invalid: %w", name, err)
		}
	}
	return value, nil
}

func validateMountedSecret(value string, key bool) error {
	lower := strings.ToLower(value)
	for _, marker := range []string{"change-me", "changeme", "placeholder", "example", "fixture", "password", "dev-secret", "secret-token"} {
		if strings.Contains(lower, marker) {
			return errors.New("known development or placeholder value")
		}
	}
	if len(value) < 32 {
		return errors.New("fewer than 32 bytes")
	}
	characters := make(map[rune]struct{})
	for _, character := range value {
		characters[character] = struct{}{}
	}
	if len(characters) < 16 {
		return errors.New("insufficient character diversity")
	}
	if key {
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(decoded) != 32 || base64.StdEncoding.EncodeToString(decoded) != value {
			return errors.New("must be canonical base64 encoding of exactly 32 bytes")
		}
		bytes := make(map[byte]struct{})
		for _, item := range decoded {
			bytes[item] = struct{}{}
		}
		if len(bytes) < 16 || string(decoded[:16]) == string(decoded[16:]) {
			return errors.New("decoded key has insufficient randomness")
		}
	}
	return nil
}
