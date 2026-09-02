package emailauthoring

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type emailTemplateTargetSnapshot struct {
	Subject   *string
	UpdatedAt time.Time
}

func TestEmailTemplateSourceSubjectNoopPreservesRevisionFactsAndTarget(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	for _, statement := range []string{
		`CREATE TABLE email_template (
			id TEXT PRIMARY KEY, source_locale TEXT NOT NULL
		)`,
		`CREATE TABLE email_template_translation (
			entity_id TEXT, locale TEXT, subject TEXT,
			content_html TEXT, content_text TEXT,
			created_at DATETIME, updated_at DATETIME,
			PRIMARY KEY (entity_id, locale)
		)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}

	now := time.Unix(1_700_000_000, 0).UTC()
	require.NoError(t, db.Exec(`INSERT INTO email_template (id, source_locale)
		VALUES ('template-1', 'en')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO email_template_translation
		(entity_id, locale, subject, created_at, updated_at)
		VALUES
		('template-1', 'en', 'Source subject', ?, ?),
		('template-1', 'ko', 'Old target', ?, ?)`,
		now, now, now, now,
	).Error)

	readTarget := func(locale string) emailTemplateTargetSnapshot {
		var snapshot emailTemplateTargetSnapshot
		require.NoError(t, db.Table("email_template_translation").
			Where("entity_id = 'template-1' AND locale = ?", locale).Take(&snapshot).Error)
		return snapshot
	}
	sourceBeforeEdit := readTarget("en")
	targetBeforeEdit := readTarget("ko")
	effect, err := updateCampaignEmailLocaleSubject(
		t.Context(), db, emailTemplateContentEntity, "template-1", "en",
		"Source subject", now.Add(time.Minute),
	)
	require.NoError(t, err)
	require.False(t, effect.Changed)
	require.False(t, effect.AffectsTranslationSource)
	require.Equal(t, sourceBeforeEdit, readTarget("en"))
	require.Equal(t, targetBeforeEdit, readTarget("ko"))

	effect, err = updateCampaignEmailLocaleSubject(
		t.Context(), db, emailTemplateContentEntity, "template-1", "en",
		"Edited source subject", now.Add(3*time.Minute),
	)
	require.NoError(t, err)
	require.True(t, effect.Changed)
	require.True(t, effect.AffectsTranslationSource)
	require.Equal(t, []string{"en"}, effect.ChangedLocales)
	sourceAfterEdit := readTarget("en")
	require.NotEqual(t, sourceBeforeEdit, sourceAfterEdit)
	require.Equal(t, "Edited source subject", *sourceAfterEdit.Subject)
	require.Equal(t, targetBeforeEdit, readTarget("ko"))
}
