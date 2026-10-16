// site-variable-migrate backfills durable email layout markers before converting
// live authored literals to site references.
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
	apply := flag.Bool("apply", false, "backfill layout markers and apply site-variable changes (one transaction per owner)")
	dryRun := flag.Bool("dry-run", true, "report proposed changes without writing (default)")
	contactEmail := flag.Bool("contact-email", false, "unify only legacy contact atoms and tokens; preserve unrelated literals and layout markers")
	writersStopped := flag.Bool("writers-stopped", false, "confirm all content writers and collaboration rooms are stopped before contact migration apply")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(2)
	}
	_ = dryRun // --apply is the only way to enable writes.
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
		if *contactEmail {
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
