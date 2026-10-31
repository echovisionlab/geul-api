package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"

	"github.com/lib/pq"
)

var appIntegrationTemplateNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// LockAppIntegrationTemplate stops new connections to a prepared template so
// background sessions such as autovacuum cannot block concurrent clones.
func LockAppIntegrationTemplate(ctx context.Context, adminDSN, templateName string) error {
	if !appIntegrationTemplateNamePattern.MatchString(templateName) {
		return fmt.Errorf("integration template must be a safe PostgreSQL identifier")
	}
	admin, err := sql.Open("postgres", adminDSN)
	if err != nil {
		return fmt.Errorf("open integration template admin connection: %w", err)
	}
	defer func() { _ = admin.Close() }()
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(
		"ALTER DATABASE %s WITH ALLOW_CONNECTIONS false", pq.QuoteIdentifier(templateName),
	)); err != nil {
		return fmt.Errorf("lock integration template %s: %w", templateName, err)
	}
	return nil
}
