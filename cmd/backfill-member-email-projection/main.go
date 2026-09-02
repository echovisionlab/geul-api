package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/echovisionlab/geul-api/internal/account"
	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/config"
)

func main() {
	ctx := context.Background()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	db, err := gorm.Open(postgres.Open(cfg.DatabaseDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	sqlDB, err := db.DB()
	if err != nil {
		slog.Error("failed to get sql db", "error", err)
		os.Exit(1)
	}
	defer sqlDB.Close()

	pageSize := 100
	if raw := os.Getenv("MEMBER_EMAIL_BACKFILL_PAGE_SIZE"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			slog.Error("invalid MEMBER_EMAIL_BACKFILL_PAGE_SIZE", "value", raw)
			os.Exit(1)
		}
		pageSize = value
	}
	result, err := account.NewAccountEmailService(db, auth.NewKratosClient(cfg.KratosAdminURL), accountadapter.MemberEmailProjection{}).
		BackfillMemberEmailProjection(ctx, pageSize)
	if err != nil {
		slog.Error("Member email projection backfill failed", "error", err)
		os.Exit(1)
	}
	slog.Info("Member email projection backfill completed", "processed", result.Processed, "synced", result.Synced, "failed", result.Failed)
	if result.Failed > 0 {
		os.Exit(1)
	}
}
