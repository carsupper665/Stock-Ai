package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"backend/internal/config"
	"backend/internal/logging"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Open 依設定連上資料庫。正式環境用 postgres，測試環境用 sqlite。
// 所有 SQL 都會記到傳入的 logger，跟 API 的日誌分開。
func Open(cfg *config.Config, log *logging.Logger) (*gorm.DB, error) {
	var dialector gorm.Dialector
	switch cfg.DBDriver {
	case "sqlite":
		dialector = sqlite.Open(cfg.DBDSN)
	case "postgres":
		dialector = postgres.Open(cfg.DBDSN)
	default:
		return nil, fmt.Errorf("不支援的 DB_DRIVER: %q", cfg.DBDriver)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: &gormLog{log: log, slow: 200 * time.Millisecond, debug: cfg.Debug},
	})
	if err != nil {
		return nil, fmt.Errorf("連線資料庫失敗 (%s): %w", cfg.DBDriver, err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("取得資料庫連線失敗: %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("資料庫無回應 (%s): %w", cfg.DBDriver, err)
	}

	log.Infof(context.Background(), "已連線 %s", cfg.DBDriver)
	return db, nil
}

// gormLog 把 gorm 的日誌介面接到我們自己的 logger。
type gormLog struct {
	log   *logging.Logger
	slow  time.Duration
	debug bool
}

func (g *gormLog) LogMode(gormlogger.LogLevel) gormlogger.Interface { return g }

func (g *gormLog) Info(ctx context.Context, msg string, args ...any) {
	g.log.Infof(ctx, msg, args...)
}

func (g *gormLog) Warn(ctx context.Context, msg string, args ...any) {
	g.log.Warnf(ctx, msg, args...)
}

func (g *gormLog) Error(ctx context.Context, msg string, args ...any) {
	g.log.Errorf(ctx, msg, args...)
}

// Trace 記錄每一句 SQL。錯誤一定記，慢查詢記警告，其餘只在 debug 模式下記。
func (g *gormLog) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	elapsed := time.Since(begin)
	sql, rows := fc()

	switch {
	case err != nil && !errors.Is(err, gorm.ErrRecordNotFound):
		g.log.Errorf(ctx, "%v | %s | rows=%d | %v", elapsed.Round(time.Microsecond), sql, rows, err)
	case elapsed > g.slow:
		g.log.Warnf(ctx, "慢查詢 %v | %s | rows=%d", elapsed.Round(time.Microsecond), sql, rows)
	case g.debug:
		g.log.Debugf(ctx, "%v | %s | rows=%d", elapsed.Round(time.Microsecond), sql, rows)
	}
}
