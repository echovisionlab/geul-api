package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/echovisionlab/geul-api/internal/account"
	accountpostgres "github.com/echovisionlab/geul-api/internal/account/postgres"
	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/telemetry"
)

func main() {
	ctx := context.Background()
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(os.Stdout, nil))))
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "failure_cause", failurecause.Message(err))
		os.Exit(1)
	}
	db, err := gorm.Open(postgres.Open(cfg.DatabaseDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		slog.Error("failed to connect to database", "failure_cause", failurecause.Message(err))
		os.Exit(1)
	}
	sqlDB, err := db.DB()
	if err != nil {
		slog.Error("failed to get sql db", "failure_cause", failurecause.Message(err))
		os.Exit(1)
	}
	defer func() {
		if err := sqlDB.Close(); err != nil {
			slog.Error("failed to close database", "failure_cause", failurecause.Message(err))
		}
	}()

	pageSize := 100
	if raw := os.Getenv("MEMBER_EMAIL_BACKFILL_PAGE_SIZE"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			if err == nil {
				err = fmt.Errorf("page size must be positive")
			}
			slog.Error("invalid MEMBER_EMAIL_BACKFILL_PAGE_SIZE", "failure_cause", failurecause.Message(err), "reason", "must_be_positive_integer")
			os.Exit(1)
		}
		pageSize = value
	}
	units, err := accountpostgres.NewEmailProjectionUnits(db, accountadapter.MemberEmailProjection{})
	if err != nil {
		slog.Error("Member email projection backfill failed", "failure_cause", failurecause.Message(err))
		os.Exit(1)
	}
	result, err := account.NewAccountEmailService(auth.NewKratosClient(cfg.KratosAdminURL), units).
		BackfillMemberEmailProjection(ctx, pageSize)
	if err != nil {
		slog.Error("Member email projection backfill failed", "failure_cause", failurecause.Message(err))
		os.Exit(1)
	}
	slog.Info("Member email projection backfill completed", "processed", result.Processed, "synced", result.Synced, "failed", result.Failed)
	if result.Failed > 0 {
		os.Exit(1)
	}
}
