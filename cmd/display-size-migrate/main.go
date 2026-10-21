// display-size-migrate renames the stored block field previewWidth to
// displaySize in live content and version snapshots, with row backups.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/echovisionlab/geul-api/internal/displaysizemigrate"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	apply := flag.Bool("apply", false, "apply the rename (one transaction per document or version row)")
	verify := flag.Bool("verify", false, "read-only gate: fail while any live Block or version snapshot still needs conversion")
	writersStopped := flag.Bool("writers-stopped", false, "confirm all content writers and collaboration rooms are stopped before apply")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(2)
	}
	if *verify && *apply {
		fmt.Fprintln(os.Stderr, "--verify cannot be combined with --apply")
		os.Exit(2)
	}
	if *apply && !*writersStopped {
		fmt.Fprintln(os.Stderr, "--apply requires --writers-stopped after stopping content writers and closing collaboration rooms")
		os.Exit(2)
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(2)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err == nil {
		if *verify {
			err = displaysizemigrate.Verify(context.Background(), db, os.Stdout)
		} else {
			err = displaysizemigrate.Run(context.Background(), db, *apply, os.Stdout)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
