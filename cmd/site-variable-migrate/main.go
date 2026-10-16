// site-variable-migrate runs explicit backed-up content maintenance operations.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/echovisionlab/geul-api/internal/sitevariablemigrate"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	apply := flag.Bool("apply", false, "apply the selected migration (one transaction per owner)")
	dryRun := flag.Bool("dry-run", true, "report proposed changes without writing (default)")
	contactEmail := flag.Bool("contact-email", false, "convert contact atoms/tokens and reviewed history catalogs; preserve unrelated literals and layout markers")
	verify := flag.Bool("verify", false, "read-only contact cutover gate: fail on remaining live/history changes or active delivery blockers")
	writersStopped := flag.Bool("writers-stopped", false, "confirm all content writers and collaboration rooms are stopped before contact migration apply")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(2)
	}
	_ = dryRun // --apply is the only way to enable writes.
	if *verify && (!*contactEmail || *apply) {
		fmt.Fprintln(os.Stderr, "--verify requires --contact-email and cannot be combined with --apply")
		os.Exit(2)
	}
	if *contactEmail && *apply && !*writersStopped {
		fmt.Fprintln(os.Stderr, "--contact-email --apply requires --writers-stopped after stopping content writers and closing collaboration rooms")
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
			err = sitevariablemigrate.VerifyContactEmail(context.Background(), db, os.Stdout)
		} else if *contactEmail {
			err = sitevariablemigrate.RunContactEmail(context.Background(), db, *apply, os.Stdout)
		} else {
			err = sitevariablemigrate.Run(context.Background(), db, *apply, os.Stdout)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
