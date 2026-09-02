package og

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestResolveExactLocalizedGenerationUsesTargetGenerationWithoutSourceCurrentnessGate(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE og_generation_target (
			id text, entity_type text, entity_id text, target_kind text,
			locale text, latest_generation_id text
		);
		CREATE TABLE og_generation (id text, status text, entity_snapshot blob);
	`).Error)
	entityID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO og_generation_target (
		id, entity_type, entity_id, target_kind, locale, latest_generation_id
	) VALUES ('target', 'work', ?, 'locale', 'ko', 'generation')`, entityID).Error)
	require.NoError(t, db.Exec(`INSERT INTO og_generation (id, status, entity_snapshot)
		VALUES ('generation', 'queued', '{}')`).Error)

	disposition, err := ResolveExactLocalizedGeneration(t.Context(), db, "work", entityID, "ko")
	require.NoError(t, err)
	require.Equal(t, LocalizedGenerationPending, disposition)

	require.NoError(t, db.Table("og_generation").Where("id = 'generation'").Update("status", "failed").Error)
	disposition, err = ResolveExactLocalizedGeneration(t.Context(), db, "work", entityID, "ko")
	require.NoError(t, err)
	require.Equal(t, LocalizedGenerationTerminal, disposition)

	require.NoError(t, db.Table("og_generation_target").Where("id = 'target'").Delete(nil).Error)
	disposition, err = ResolveExactLocalizedGeneration(t.Context(), db, "work", entityID, "ko")
	require.NoError(t, err)
	require.Equal(t, LocalizedGenerationMissing, disposition)
}
