package utils

import (
    "errors"
    "os"

    "github.com/joho/godotenv"
)

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
    SQLitePath = "DB.db?_busy_timeout=5000"
)

func LoadEnv() error {
    if err := godotenv.Load(".env"); err != nil {
        var pathErr *os.PathError
        if !errors.As(err, &pathErr) {
            return err
        }
    }

    DebugMode = GetEnvBool("DEBUG", false)
    DCWebHookUrl = GetEnvString("DC_WEB_HOOK", "")
    SessionSecret = GetEnvString("SESSION_SECRET", "")
    if SessionSecret == "" {
        SessionSecret = "123456789"
        if SysLog != nil {
            SysLog.Warn("SESSION_SECRET is empty")
        }
    }
    FrontEndUrl = GetEnvString("FRONTEND_BASE_URL", "http://localhost:3000")
    PostgreDSN = GetEnvString("POSTGRES_DSN", "")
    RootUser = GetEnvString("ROOT_USER", "")
    RootUserEmail = GetEnvString("ROOT_USER_EMAIL", GetEnvString("ROOT_EMAIL_NAME", ""))
    RootPassword = GetEnvString("ROOT_PASSWORD", "123")

    return nil
}
