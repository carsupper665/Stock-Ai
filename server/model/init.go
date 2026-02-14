package model

import (
	"server/model/store"
	"server/utils"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

var DB *gorm.DB
var logger *utils.SysLogger

func InitDb() error {
	logger = utils.SysLog

	db, err := Factory()
	if err != nil {
		logger.Errorf("Failed to connect to database: %v", err)
		return err
	}
	DB = db
	if err := migrateDB(); err != nil {
		logger.Errorf("Failed to migrate database: %v", err)
		return err
	}

	if utils.RootUser == "" || utils.RootUserEmail == "" {
		logger.Info("Root user data not set\n if you want create root user please set ROOT_USER_NAME and ROOT_EMAIL_NAME in .env")
	} else if RootUserExists() {
		logger.Info("Root User Exists, skip create root user")
	} else {
		if err := createRoot(); err != nil {
			logger.Errorf("Failed to create root user: %v", err)
		}
	}

	logger.Info("Database migrated")
	return nil
}

func Factory() (*gorm.DB, error) {
	var err error
	var db *gorm.DB

	dsn := utils.PostgreDSN
	if dsn == "" {
		db, err = initSqliteDB()
		return db, err
	}
	db, err = initPostgreSQLDB(dsn, true)

	return db, nil
}
func initSqliteDB() (*gorm.DB, error) {
	return gorm.Open(sqlite.Open(utils.SQLitePath), &gorm.Config{
		PrepareStmt: true, // precompile SQL
	})
}

func initPostgreSQLDB(dsn string, isLog bool) (*gorm.DB, error) {
	cfg := &gorm.Config{}
	if isLog {
		cfg.Logger = gormLogger.Default.LogMode(gormLogger.Info)
	}

	db, err := gorm.Open(postgres.Open(dsn), cfg)
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err == nil {
		sqlDB.SetMaxIdleConns(10)
		sqlDB.SetMaxOpenConns(100)
		sqlDB.SetConnMaxLifetime(time.Hour)
	}

	return db, nil
}

func migrateDB() error {
	err := DB.AutoMigrate(
		&store.Account{},
		&store.User{},
		&store.LLMUser{},
	)
	return err
}
func createRoot() error {
	username := utils.RootUser
	email := utils.RootUserEmail
	password := utils.RootPassword
	salt := utils.GetRandomString(16)

	sp := password + salt
	hashPassword, err := utils.P2H(sp)
	if err != nil {
		return err
	}

	// create user
	rootUser := store.User{
		Username:    username,
		DisplayName: "Root User",
		Role:        utils.RoleRootUser,
		Email:       email,
		Password:    hashPassword,
		Salt:        salt,
	}

	err = DB.Create(&rootUser).Error
	if err != nil {
		return err
	}
	return nil
}

func RootUserExists() bool {
	var user store.User
	err := DB.Where("role = ?", utils.RoleRootUser).First(&user).Error
	return err == nil
}
