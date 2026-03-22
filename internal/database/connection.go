package database

import (
	"database/sql"
	"fmt"

	_ "github.com/go-sql-driver/mysql"

	"github.com/magendooro/magento2-catalog-graphql-go/internal/config"
)

const dsnParams = "parseTime=true&charset=utf8mb4&collation=utf8mb4_unicode_ci&loc=UTC&time_zone=%27%2B00%3A00%27"

func NewConnection(cfg config.DatabaseConfig) (*sql.DB, error) {
	var dsn string
	if cfg.Host == "localhost" {
		// Unix socket (matches Magento's "localhost" behavior)
		dsn = fmt.Sprintf("%s:%s@unix(/tmp/mysql.sock)/%s?%s",
			cfg.User, cfg.Password, cfg.Name, dsnParams,
		)
	} else {
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?%s",
			cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Name, dsnParams,
		)
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return db, nil
}
