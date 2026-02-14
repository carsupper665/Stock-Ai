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

var (
	DebugMode     bool
	RootUser      string
	RootUserEmail string
	RootPassword  string
)

var (
	PostgreDSN string
	SQLitePath = "DB.db?_busy_timeout=5000" // Sql Lite File Path
)

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
	PostgreDSN = GetEnvString("POSTGRES_DSN", "")
	RootUser = GetEnvString("ROOT_USER", "")
	RootPassword = GetEnvString("ROOT_PASSWORD", "123")

	return nil
}
