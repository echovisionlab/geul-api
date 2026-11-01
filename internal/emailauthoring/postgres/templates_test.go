package emailauthoringpostgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestEmailTemplateBaseRowInsertOmitsRemovedCanonicalColumns(t *testing.T) {
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=localhost user=geul dbname=geul sslmode=disable",
	}), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	require.NoError(t, err)

	now := time.Unix(1_700_000_000, 0).UTC()
	description := "Welcome"
	sql := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return tx.Omit("ID").
			Clauses(clause.Returning{}).
			Create(&emailTemplateBaseRow{
				Key:         "welcome_template",
				Name:        "Welcome Template",
				Description: &description,
				IsSystem:    true,
				IsActive:    true,
				CreatedAt:   now,
				UpdatedAt:   &now,
			})
	})
	assert.Contains(t, sql, `INSERT INTO "email_template"`)
	assert.NotContains(t, sql, `"subject"`)
	assert.NotContains(t, sql, `"content_html"`)
	assert.NotContains(t, sql, `"yjs_state"`)
}
