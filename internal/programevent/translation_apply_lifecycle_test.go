package programevent

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestProgramEventTranslationApplyFenceAllowsNonDeletedLifecycleStates(t *testing.T) {
	db := openProgramEventTranslationApplyLifecycleDB(t)

	for _, status := range []string{
		managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT.String(),
		managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String(),
	} {
		t.Run(status, func(t *testing.T) {
			eventID := uuid.NewString()
			documentID := uuid.New()
			require.NoError(t, db.Exec(
				`INSERT INTO program_event (id, status, source_locale, content_document_id) VALUES (?, ?, ?, ?)`,
				eventID, status, "en", documentID.String(),
			).Error)

			domain, err := runProgramEventTranslationApplyFence(t.Context(), db, eventID, documentID)
			require.NoError(t, err)
			require.Equal(t, "en", domain.SourceLocale)
		})
	}
}

func TestProgramEventTranslationApplyFenceRejectsDeletedRoot(t *testing.T) {
	db := openProgramEventTranslationApplyLifecycleDB(t)
	eventID := uuid.NewString()
	documentID := uuid.New()
	require.NoError(t, db.Exec(
		`INSERT INTO program_event (id, status, source_locale, content_document_id) VALUES (?, ?, ?, ?)`,
		eventID, managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT.String(), "en", documentID.String(),
	).Error)
	require.NoError(t, db.Exec(`DELETE FROM program_event WHERE id = ?`, eventID).Error)

	_, err := runProgramEventTranslationApplyFence(t.Context(), db, eventID, documentID)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func openProgramEventTranslationApplyLifecycleDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE program_event (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			source_locale TEXT NOT NULL,
			content_document_id TEXT
		)
	`).Error)
	return db
}

func runProgramEventTranslationApplyFence(
	ctx context.Context,
	db *gorm.DB,
	eventID string,
	documentID uuid.UUID,
) (contentblock.DomainContext, error) {
	if err := lockTranslationEntityRootWithDB(ctx, db, EntityType, eventID); err != nil {
		return contentblock.DomainContext{}, err
	}
	return programEventContentDocumentFence(eventID, func(ctx context.Context, tx *gorm.DB) error {
		return RequireExists(ctx, tx, eventID)
	})(ctx, db, documentID)
}
