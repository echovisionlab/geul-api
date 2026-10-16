package publiccontent

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLocaleAvailabilityFailureLogsCauseAndKeepsSelection(t *testing.T) {
	db := newLocalizationTestDB(t)
	id := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO series(id,source_locale) VALUES(?,'en')`, id).Error)
	require.NoError(t, db.Exec(`INSERT INTO series_translation(entity_id,locale,title) VALUES(?,'en','Source')`, id).Error)
	cause := errors.New("locale availability query: connection reset")
	callback := "test:availability_failure"
	require.NoError(t, db.Callback().Row().Before("gorm:row").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "series_translation" && len(tx.Statement.Selects) == 1 && tx.Statement.Selects[0] == "locale" {
			tx.AddError(cause)
		}
	}))
	t.Cleanup(func() { db.Callback().Row().Remove(callback) })
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	selection, err := ResolveWithPolicy(t.Context(), db, Spec{EntityType: "series", TableName: "series_translation", SelectClause: "locale, title"}, id, "en", translation.DefaultRuntimeSettings())
	require.NoError(t, err)
	require.Equal(t, "en", selection.DisplayedLocale)
	require.Equal(t, "Source", *selection.Title)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, cause.Error(), entry["failure_cause"])
	require.Equal(t, id, entry["entity_id"])
}
