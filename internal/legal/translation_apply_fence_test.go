package legal

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestLegalTranslationJobApplyDocumentFenceUsesRootExistenceOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	for _, statement := range []string{
		`CREATE TABLE terms_history (
			id TEXT PRIMARY KEY, title TEXT, status TEXT NOT NULL,
			version INTEGER, content_document_id TEXT, source_locale TEXT NOT NULL
		)`,
		`CREATE TABLE privacy_history (
			id TEXT PRIMARY KEY, title TEXT, status TEXT NOT NULL,
			version INTEGER, content_document_id TEXT, source_locale TEXT NOT NULL
		)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}

	testCases := []struct {
		kind   string
		status string
	}{
		{kind: "terms", status: managev1.TermsStatus_TERMS_STATUS_DRAFT.String()},
		{kind: "terms", status: managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String()},
		{kind: "terms", status: managev1.TermsStatus_TERMS_STATUS_ACTIVE.String()},
		{kind: "terms", status: managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String()},
		{kind: "privacy", status: managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT.String()},
		{kind: "privacy", status: managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String()},
		{kind: "privacy", status: managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String()},
		{kind: "privacy", status: managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String()},
	}
	for _, testCase := range testCases {
		t.Run(testCase.kind+"/"+testCase.status, func(t *testing.T) {
			entityID := uuid.NewString()
			documentID := uuid.New()
			require.NoError(t, db.Exec(
				"INSERT INTO "+testCase.kind+"_history (id, title, status, version, content_document_id, source_locale) VALUES (?, 'Policy', ?, 1, ?, 'en')",
				entityID, testCase.status, documentID,
			).Error)

			domain, err := legalTranslationJobApplyDocumentFence(
				testCase.kind,
				entityID,
			)(t.Context(), db, documentID)
			require.NoError(t, err)
			require.Equal(t, "en", domain.SourceLocale)
		})
	}

	for _, kind := range []string{"terms", "privacy"} {
		entityID := uuid.NewString()
		documentID := uuid.New()
		require.NoError(t, db.Exec(
			"INSERT INTO "+kind+"_history (id, title, status, version, content_document_id, source_locale) VALUES (?, 'Deleted policy', 'draft', 1, ?, 'en')",
			entityID, documentID,
		).Error)
		require.NoError(t, db.Exec("DELETE FROM "+kind+"_history WHERE id = ?", entityID).Error)
		_, err := legalTranslationJobApplyDocumentFence(
			kind,
			entityID,
		)(t.Context(), db, documentID)
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	}
}
