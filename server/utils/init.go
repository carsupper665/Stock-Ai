package utils

import "github.com/joho/godotenv"

var SysLog *SysLogger

func InitLogger(logName string, maxLog int) error {
	var err error
	SysLog, err = NewSysLogger(logName, maxLog)
	if err != nil {
		return err
	}
	return nil
}

func LoadEnv() error {
	if err := godotenv.Load(".env"); err != nil {
		return err
	}

	DebugMode = GetEnvBool("DEBUG", false)
	DCWebHookUrl = GetEnvString("DC_WEB_HOOK", "")
	SessionSecret = GetEnvString("SESSION_SECRET", "")
	if SessionSecret == "" {
		SysLog.Warn("SESSION_SECRET is empty")
		SessionSecret = "123456789"
	}
	FrontEndUrl = GetEnvString("FRONTEND_BASE_URL", "http://localhost:3000")

	return nil
}
