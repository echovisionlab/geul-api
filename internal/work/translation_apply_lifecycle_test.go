package work

import (
	"testing"

	"connectrpc.com/connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestWorkTranslationApplyFenceAllowsNonDeletedLifecycleStates(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE work (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			content_document_id TEXT,
			source_locale TEXT NOT NULL
		)
	`).Error)

	for _, status := range []string{
		managev1.WorkStatus_WORK_STATUS_DRAFT.String(),
		managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(),
	} {
		t.Run(status, func(t *testing.T) {
			workID := uuid.NewString()
			documentID := uuid.New()
			require.NoError(t, db.Exec(
				`INSERT INTO work (id, status, content_document_id, source_locale) VALUES (?, ?, ?, ?)`,
				workID, status, documentID.String(), "en",
			).Error)

			fence := workSystemTranslationDocumentFence(workID)
			domain, err := fence(t.Context(), db, documentID)
			require.NoError(t, err)
			require.Equal(t, "en", domain.SourceLocale)
		})
	}
}

func TestWorkTranslationApplyFenceRejectsDeletedRoot(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE work (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			content_document_id TEXT,
			source_locale TEXT NOT NULL
		)
	`).Error)

	workID := uuid.NewString()
	documentID := uuid.New()
	require.NoError(t, db.Exec(
		`INSERT INTO work (id, status, content_document_id, source_locale) VALUES (?, ?, ?, ?)`,
		workID, managev1.WorkStatus_WORK_STATUS_DRAFT.String(), documentID.String(), "en",
	).Error)
	require.NoError(t, db.Exec(`DELETE FROM work WHERE id = ?`, workID).Error)

	_, err = workSystemTranslationDocumentFence(workID)(t.Context(), db, documentID)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}
