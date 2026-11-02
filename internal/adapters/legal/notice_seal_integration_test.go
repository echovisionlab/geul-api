//go:build integration

package legal

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

// TestNoticeRuntimeSealsTheActiveTemplateOfTheVersionIntegration pins what
// a legal notice run keeps of its source: the version and its number, the
// active template the schema seeds for each legal event, and the exact
// template data; an event without an active template seals nothing.
func TestNoticeRuntimeSealsTheActiveTemplateOfTheVersionIntegration(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		kind     string
		eventKey string
		data     map[string]string
	}{
		{kind: "terms", eventKey: "terms_effective", data: map[string]string{"terms_url": "https://example.test/terms"}},
		{kind: "privacy", eventKey: "privacy_update", data: map[string]string{
			"policy_title": "Privacy", "effective_date": "2026-12-01", "preview_url": "https://example.test/s/token",
		}},
	} {
		t.Run(testCase.kind, func(t *testing.T) {
			t.Parallel()
			db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
			policyID := uuid.NewString()
			documentID := uuid.NewString()
			require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?, 'policy')`, documentID).Error)
			require.NoError(t, db.Exec(
				`INSERT INTO `+testCase.kind+`_history (id, version, content, content_document_id) VALUES (?, 7, '', ?)`,
				policyID, documentID,
			).Error)
			scheduledAt := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)

			var run *model.CampaignDeliveryRun
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				var err error
				run, err = NewNoticeRuntime().CreateRun(t.Context(), tx, testCase.kind, policyID, testCase.eventKey, testCase.data, scheduledAt)
				return err
			}))
			var stored model.CampaignDeliveryRun
			require.NoError(t, db.First(&stored, "id = ?", run.ID).Error)
			require.True(t, stored.DefinitionSealed)
			require.Equal(t, "legal_notice", stored.RunKind)
			require.Equal(t, "scheduled", stored.Status)
			require.True(t, scheduledAt.Equal(stored.ScheduledAt))
			require.Equal(t, testCase.eventKey, *stored.TemplateEventKey)
			require.NotNil(t, stored.SourceTemplateID)
			require.Nil(t, stored.SourceLayoutID)
			for key, value := range testCase.data {
				require.Equal(t, value, stored.TemplateData[key])
			}
			if testCase.kind == "terms" {
				require.Equal(t, policyID, *stored.TermsID)
				require.EqualValues(t, 7, *stored.SourceTermsVersion)
				require.Nil(t, stored.PrivacyID)
			} else {
				require.Equal(t, policyID, *stored.PrivacyID)
				require.EqualValues(t, 7, *stored.SourcePrivacyVersion)
				require.Nil(t, stored.TermsID)
			}

			require.NoError(t, db.Model(&model.EmailTemplate{}).Where("event_key = ?", testCase.eventKey).Update("event_key", nil).Error)
			err := db.Transaction(func(tx *gorm.DB) error {
				_, err := NewNoticeRuntime().CreateRun(t.Context(), tx, testCase.kind, policyID, testCase.eventKey, testCase.data, scheduledAt)
				return err
			})
			require.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(err))
		})
	}
}
