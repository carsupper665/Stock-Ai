package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"backend/internal/config"
	"backend/internal/logging"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// 資料存取的共用錯誤，讓上層不必認得 gorm 或各家驅動的細節。
var (
	ErrNotFound  = errors.New("資料不存在")
	ErrDuplicate = errors.New("資料重複")
)

// Store 是所有資料庫操作的唯一入口。
type Store struct {
	db *gorm.DB
}

// Open 依設定連上資料庫。正式環境用 postgres，測試環境用 sqlite。
// 所有 SQL 都會記到傳入的 logger，跟 API 的日誌分開。
func Open(cfg *config.Config, log *logging.Logger) (*Store, error) {
	var dialector gorm.Dialector
	switch cfg.DBDriver {
	case "sqlite":
		dialector = sqlite.Open(cfg.DBDSN)
	case "postgres":
		dialector = postgres.Open(cfg.DBDSN)
	default:
		return nil, fmt.Errorf("不支援的 DB_DRIVER: %q", cfg.DBDriver)
	}

	db, err := gorm.Open(dialector, gormConfig(&gormLog{log: log, slow: 200 * time.Millisecond, debug: cfg.Debug}))
	if err != nil {
		return nil, fmt.Errorf("連線資料庫失敗 (%s): %w", cfg.DBDriver, err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("取得資料庫連線失敗: %w", err)
	}
	if cfg.DBDriver == "sqlite" {
		// sqlite 同時只容得下一個寫入者，多條連線會互相卡成 database is locked。
		sqlDB.SetMaxOpenConns(1)
	}

	store := &Store{db: db}
	if err := store.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("資料庫無回應 (%s): %w", cfg.DBDriver, err)
	}

	log.Infof(context.Background(), "已連線 %s", cfg.DBDriver)
	return store, nil
}

// OpenSQLite 直接開一個 sqlite 檔案且不記日誌，供測試與工具使用。
func OpenSQLite(path string) (*Store, error) {
	db, err := gorm.Open(sqlite.Open(path), gormConfig(gormlogger.Discard))
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	return &Store{db: db}, nil
}

// gormConfig 統一所有連線的設定。時間一律以 UTC 寫入：sqlite 把時間存成
// 帶時區的文字，範圍查詢是字串比大小，寫入與查詢的時區不一致就會靜默出錯。
func gormConfig(logger gormlogger.Interface) *gorm.Config {
	return &gorm.Config{
		Logger:         logger,
		TranslateError: true,
		NowFunc:        func() time.Time { return time.Now().UTC() },
	}
}

// Close 關閉資料庫連線。
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Ping 確認資料庫連線仍然可用。
func (s *Store) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// Migrate 建立或更新資料表結構。新增 model 時加進這個清單。
func (s *Store) Migrate() error {
	if err := s.db.AutoMigrate(&Account{}, &Order{}, &Position{}, &Trade{}, &Message{}, &MessageTag{}, &LedgerEntry{}); err != nil {
		return fmt.Errorf("建立資料表失敗: %w", err)
	}
	return nil
}

// translate 把 gorm 與各家驅動的錯誤收斂成本套件的錯誤。
func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return ErrNotFound
	case errors.Is(err, gorm.ErrDuplicatedKey), isUniqueViolation(err):
		return ErrDuplicate
	default:
		return err
	}
}

// isUniqueViolation 比對驅動回報的唯一鍵衝突訊息。
// sqlite 與 postgres 的措辭不同，兩邊都要認。
func isUniqueViolation(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate key")
}

// gormLog 把 gorm 的日誌介面接到我們自己的 logger。
type gormLog struct {
	log   *logging.Logger
	slow  time.Duration
	debug bool
}

var _ gorm.ParamsFilter = (*gormLog)(nil)

func (g *gormLog) LogMode(gormlogger.LogLevel) gormlogger.Interface { return g }

func (g *gormLog) Info(ctx context.Context, msg string, args ...any) {
	g.log.Infof(ctx, msg, args...)
}

func (g *gormLog) Warn(ctx context.Context, msg string, args ...any) {
	g.log.Warnf(ctx, msg, args...)
}

func (g *gormLog) Error(ctx context.Context, _ string, args ...any) {
	category := "database"
	for _, arg := range args {
		if err, ok := arg.(error); ok {
			category = databaseErrorCategory(err)
			break
		}
	}
	g.log.Errorf(ctx, "gorm error=%s", category)
}

// ParamsFilter is called by GORM before Dialector.Explain. Returning no parameters keeps
// placeholders in the logged SQL so authority-bearing Account Tokens never reach logs.
func (g *gormLog) ParamsFilter(_ context.Context, sql string, _ ...interface{}) (string, []interface{}) {
	return sql, nil
}

// Trace 記錄每一句 SQL。錯誤一定記，慢查詢記警告，其餘只在 debug 模式下記。
func (g *gormLog) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	elapsed := time.Since(begin)
	sql, rows := fc()

	switch {
	case err != nil && !errors.Is(err, gorm.ErrRecordNotFound):
		g.log.Errorf(ctx, "%v | %s | rows=%d | error=%s", elapsed.Round(time.Microsecond), sql, rows, databaseErrorCategory(err))
	case elapsed > g.slow:
		g.log.Warnf(ctx, "慢查詢 %v | %s | rows=%d", elapsed.Round(time.Microsecond), sql, rows)
	case g.debug:
		g.log.Debugf(ctx, "%v | %s | rows=%d", elapsed.Round(time.Microsecond), sql, rows)
	}
}

func databaseErrorCategory(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return "duplicate"
	case errors.Is(err, gorm.ErrForeignKeyViolated):
		return "foreign_key"
	default:
		return "database"
	}
}
