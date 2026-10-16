package model

import (
	"sync"
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/sqlitetest"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestEmailTemplateGlobalKeyAndEventUniqueness(t *testing.T) {
	t.Parallel()
	db, err := sqlitetest.Open(t, sqlite.Open, &gorm.Config{})
	require.NoError(t, err)

	parsed, err := schema.Parse(&EmailTemplate{}, &sync.Map{}, db.NamingStrategy)
	require.NoError(t, err)
	require.Contains(t, parsed.FieldsByName["Key"].TagSettings, "UNIQUE")
	require.Contains(t, parsed.FieldsByName["EventKey"].TagSettings, "UNIQUE")
	require.NotContains(t, parsed.FieldsByName, "ArchivedAt")
}
