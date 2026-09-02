//go:build integration

package public_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	legalpublic "github.com/echovisionlab/geul-api/internal/legal/public"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestPublishedAndArchivedLegalHistoryFallsBackAfterTargetDeletionIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	store := newPublicLegalContentBlockStore(t)
	media := legaladapter.NewOGRuntime("", nil)
	now := time.Now().UTC()
	past := now.Add(-2 * time.Hour)
	future := now.Add(2 * time.Hour)

	for index, testCase := range []struct {
		name       string
		entityType string
		status     string
		seed       func(int, string, string, string, *time.Time, *time.Time) string
		read       func(string) (string, string, string)
	}{
		{
			name: "published privacy", entityType: "privacy",
			status: managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String(),
			seed: func(version int, title, body, status string, from, until *time.Time) string {
				return seedPublicPrivacyPolicy(t, db, store, version, title, body, status, from, until).ID
			},
			read: func(id string) (string, string, string) {
				request := connect.NewRequest(&openv1.GetPrivacyRequest{Id: &id})
				request.Header().Set("Accept-Language", "ko")
				response, err := legalpublic.NewPrivacyServiceWithContentBlocks(db, store, media).Get(t.Context(), request)
				require.NoError(t, err)
				return response.Msg.Privacy.GetTitle(),
					publicLegalDocumentText(response.Msg.Privacy.GetDocument()),
					response.Msg.Privacy.GetLocalizationInfo().GetDisplayedLocale()
			},
		},
		{
			name: "archived privacy", entityType: "privacy",
			status: managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String(),
			seed: func(version int, title, body, status string, from, until *time.Time) string {
				return seedPublicPrivacyPolicy(t, db, store, version, title, body, status, from, until).ID
			},
			read: func(id string) (string, string, string) {
				request := connect.NewRequest(&openv1.GetPrivacyRequest{Id: &id})
				request.Header().Set("Accept-Language", "ko")
				response, err := legalpublic.NewPrivacyServiceWithContentBlocks(db, store, media).Get(t.Context(), request)
				require.NoError(t, err)
				return response.Msg.Privacy.GetTitle(),
					publicLegalDocumentText(response.Msg.Privacy.GetDocument()),
					response.Msg.Privacy.GetLocalizationInfo().GetDisplayedLocale()
			},
		},
		{
			name: "published terms", entityType: "terms",
			status: managev1.TermsStatus_TERMS_STATUS_ACTIVE.String(),
			seed: func(version int, title, body, status string, from, until *time.Time) string {
				return seedPublicTermsPolicy(t, db, store, version, title, body, status, from, until).ID
			},
			read: func(id string) (string, string, string) {
				request := connect.NewRequest(&openv1.GetTermsRequest{Id: &id})
				request.Header().Set("Accept-Language", "ko")
				response, err := legalpublic.NewTermsServiceWithContentBlocks(db, store, media).Get(t.Context(), request)
				require.NoError(t, err)
				return response.Msg.Terms.GetTitle(),
					publicLegalDocumentText(response.Msg.Terms.GetDocument()),
					response.Msg.Terms.GetLocalizationInfo().GetDisplayedLocale()
			},
		},
		{
			name: "archived terms", entityType: "terms",
			status: managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String(),
			seed: func(version int, title, body, status string, from, until *time.Time) string {
				return seedPublicTermsPolicy(t, db, store, version, title, body, status, from, until).ID
			},
			read: func(id string) (string, string, string) {
				request := connect.NewRequest(&openv1.GetTermsRequest{Id: &id})
				request.Header().Set("Accept-Language", "ko")
				response, err := legalpublic.NewTermsServiceWithContentBlocks(db, store, media).Get(t.Context(), request)
				require.NoError(t, err)
				return response.Msg.Terms.GetTitle(),
					publicLegalDocumentText(response.Msg.Terms.GetDocument()),
					response.Msg.Terms.GetLocalizationInfo().GetDisplayedLocale()
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			title := "Source " + testCase.name
			body := "source body " + testCase.name
			entityID := testCase.seed(980000+index, title, body, testCase.status, &past, &future)
			require.NoError(t, db.Exec(
				"INSERT INTO "+testCase.entityType+"_translation (entity_id, locale, title, created_at, updated_at) VALUES (?, 'ko', ?, ?, ?)",
				entityID, "삭제될 번역", now, now,
			).Error)
			require.NoError(t, db.Exec(
				"DELETE FROM "+testCase.entityType+"_translation WHERE entity_id = ? AND locale = 'ko'",
				entityID,
			).Error)

			gotTitle, gotBody, displayedLocale := testCase.read(entityID)
			require.Equal(t, title, gotTitle)
			require.Equal(t, body, gotBody)
			require.Equal(t, "en", displayedLocale)
		})
	}
}
