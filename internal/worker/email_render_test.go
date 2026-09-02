package worker

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/email"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type campaignEmailRendererStub struct {
	campaignID string
	locale     string
}

func (stub *campaignEmailRendererStub) RenderCampaignEmail(
	_ context.Context,
	_ *gorm.DB,
	campaignID string,
	locale string,
	_ map[string]string,
) (*email.RenderedEmail, error) {
	stub.campaignID = campaignID
	stub.locale = locale
	return &email.RenderedEmail{Subject: "Campaign", HTML: "<p>Campaign</p>"}, nil
}

func TestRenderEmailJobTemplateRoutesLiveCampaignToCampaignRenderer(t *testing.T) {
	renderer := &campaignEmailRendererStub{}
	rendered, err := (&Handlers{campaignEmail: renderer}).renderEmailJobTemplate(
		t.Context(),
		&managev1.SendEmailEvent{TemplateType: "campaign:campaign-1"},
		"ko",
		map[string]string{},
	)
	require.NoError(t, err)
	require.Equal(t, "Campaign", rendered.Subject)
	require.Equal(t, "campaign-1", renderer.campaignID)
	require.Equal(t, "ko", renderer.locale)
}

func TestRenderEmailJobTemplateRequiresCampaignRenderer(t *testing.T) {
	rendered, err := (&Handlers{}).renderEmailJobTemplate(
		t.Context(),
		&managev1.SendEmailEvent{TemplateType: "campaign:campaign-1"},
		"en",
		map[string]string{},
	)
	require.Nil(t, rendered)
	require.ErrorContains(t, err, "campaign email renderer is required")
}

func TestRenderEmailJobTemplateUsesOnlySealedDurableSnapshotUnit(
	t *testing.T,
) {
	db := newEmailRenderUnitDB(t)
	runID := uuid.NewString()
	recipientID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO email_delivery_run (
			id, run_kind, render_snapshot, snapshot_schema_version,
			definition_sealed
		) VALUES (?, 'campaign', ?, 1, 1)`,
		runID,
		`{
			"source_locale":"en",
			"subject":"Frozen {{recipient_email}}",
			"content_html":"<p>Frozen body {{recipient_email}}</p>",
			"translations":[{
				"locale":"en",
				"subject":"Frozen {{recipient_email}}",
				"content_html":"<p>Frozen body {{recipient_email}}</p>"
			}]
		}`,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO email_delivery_recipient (id, run_id) VALUES (?, ?)`,
		recipientID,
		runID,
	).Error)

	rendered, err := (&Handlers{db: db}).renderEmailJobTemplate(
		t.Context(),
		&managev1.SendEmailEvent{DeliveryRecipientId: &recipientID},
		"ko",
		map[string]string{"recipient_email": "reader@example.com"},
	)
	require.NoError(t, err)
	require.NotNil(t, rendered)
	require.Equal(t, "Frozen reader@example.com", rendered.Subject)
	require.Contains(t, rendered.HTML, "Frozen body reader@example.com")
}

func TestRenderEmailJobTemplateRejectsInvalidDurableDefinitionUnit(
	t *testing.T,
) {
	for _, testCase := range []struct {
		name               string
		snapshot           string
		schemaVersion      int
		definitionSealed   bool
		expectedErrorMatch string
	}{
		{
			name:               "missing snapshot",
			snapshot:           `{}`,
			schemaVersion:      1,
			definitionSealed:   true,
			expectedErrorMatch: `render snapshot is missing required key "subject"`,
		},
		{
			name:               "malformed snapshot content",
			snapshot:           `{"source_locale":"en"}`,
			schemaVersion:      1,
			definitionSealed:   true,
			expectedErrorMatch: `render snapshot is missing required key "subject"`,
		},
		{
			name:               "unsealed definition",
			snapshot:           `{"subject":"Frozen","content_html":"<p>Frozen</p>"}`,
			schemaVersion:      1,
			definitionSealed:   false,
			expectedErrorMatch: "definition is not sealed",
		},
		{
			name:               "unsupported schema",
			snapshot:           `{"subject":"Frozen","content_html":"<p>Frozen</p>"}`,
			schemaVersion:      2,
			definitionSealed:   true,
			expectedErrorMatch: "unsupported email delivery snapshot schema version",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newEmailRenderUnitDB(t)
			runID := uuid.NewString()
			recipientID := uuid.NewString()
			require.NoError(t, db.Exec(
				`INSERT INTO email_delivery_run (
					id, run_kind, render_snapshot, snapshot_schema_version,
					definition_sealed
				) VALUES (?, 'campaign', ?, ?, ?)`,
				runID,
				testCase.snapshot,
				testCase.schemaVersion,
				testCase.definitionSealed,
			).Error)
			require.NoError(t, db.Exec(
				`INSERT INTO email_delivery_recipient (id, run_id) VALUES (?, ?)`,
				recipientID,
				runID,
			).Error)

			rendered, err := (&Handlers{db: db}).renderEmailJobTemplate(
				t.Context(),
				&managev1.SendEmailEvent{
					DeliveryRecipientId: &recipientID,
					TemplateType:        "must-not-resolve-live",
				},
				"en",
				map[string]string{},
			)
			require.Nil(t, rendered)
			require.ErrorContains(t, err, testCase.expectedErrorMatch)
		})
	}
}

func newEmailRenderUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE email_delivery_run (
			id text PRIMARY KEY,
			run_kind text NOT NULL,
			template_event_key text,
			template_data text NOT NULL DEFAULT '{}',
			render_snapshot text NOT NULL,
			snapshot_schema_version integer NOT NULL,
			definition_sealed integer NOT NULL
		)
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE email_delivery_recipient (
			id text PRIMARY KEY,
			run_id text NOT NULL
		)
	`).Error)
	return db
}
