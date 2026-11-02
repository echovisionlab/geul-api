package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/opentelemetry/tracing"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/failurecause"
)

func connectDatabase(ctx context.Context, dsn string, maxOpenConnections, maxIdleConnections int) (*gorm.DB, *sql.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:  logger.Default.LogMode(logger.Silent),
		NowFunc: dbtime.Now,
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
	if err := sqlDB.PingContext(ctx); err != nil {
		if closeErr := sqlDB.Close(); closeErr != nil {
			slog.ErrorContext(ctx, "Database close after failed ping failed", "failure_cause", failurecause.Message(closeErr))
		}
		return nil, nil, fmt.Errorf("ping database: %w", err)
	}
	if err := db.Use(newDatabaseTracingPlugin()); err != nil {
		slog.WarnContext(ctx, "Failed to enable GORM tracing", "failure_cause", failurecause.Message(err))
	}
	slog.InfoContext(ctx, "Connected to database")
	return db, sqlDB, nil
}

func newDatabaseTracingPlugin() gorm.Plugin {
	return tracing.NewPlugin(tracing.WithoutQueryVariables())
}
