// Package sqlitetest owns isolated, test-scoped in-memory SQLite databases.
package sqlitetest

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Open allocates a fresh database for each invocation, including repeated calls
// from the same test. Connections in its pool share that database, while other
// fixtures cannot see it. The pool closes after the test and its subtests finish.
// Pass the owning test's SQLite driver to preserve its driver-specific behavior.
func Open(t testing.TB, dialector func(string) gorm.Dialector, config *gorm.Config) (*gorm.DB, error) {
	t.Helper()
	db, err := gorm.Open(dialector("file:"+uuid.NewString()+"?mode=memory&cache=shared"), config)
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get SQLite fixture connection pool: %w", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close SQLite fixture connection pool: %v", err)
		}
	})
	return db, nil
}
