package model

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestEmailTemplateGlobalKeyAndEventUniqueness(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:email-template-schema?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)

	parsed, err := schema.Parse(&EmailTemplate{}, &sync.Map{}, db.NamingStrategy)
	require.NoError(t, err)
	require.Contains(t, parsed.FieldsByName["Key"].TagSettings, "UNIQUE")
	require.Contains(t, parsed.FieldsByName["EventKey"].TagSettings, "UNIQUE")
	require.NotContains(t, parsed.FieldsByName, "ArchivedAt")
}
