package main

import (
	"database/sql"
	"fmt"
	"log/slog"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/opentelemetry/tracing"
)

func connectDatabase(dsn string, maxOpenConnections, maxIdleConnections int) (*gorm.DB, *sql.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("connect to database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, nil, fmt.Errorf("get underlying database: %w", err)
	}
	sqlDB.SetMaxOpenConns(maxOpenConnections)
	sqlDB.SetMaxIdleConns(maxIdleConnections)
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, nil, fmt.Errorf("ping database: %w", err)
	}
	if err := db.Use(newDatabaseTracingPlugin()); err != nil {
		slog.Warn("Failed to enable GORM tracing", "error", err)
	}
	slog.Info("Connected to database")
	return db, sqlDB, nil
}

func newDatabaseTracingPlugin() gorm.Plugin {
	return tracing.NewPlugin(tracing.WithoutQueryVariables())
}
