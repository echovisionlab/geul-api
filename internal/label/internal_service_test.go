//go:build integration

package label

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

func seedInternalLabel(t *testing.T, db *gorm.DB, contentBlocks *contentblock.Store, ownerID string) string {
	t.Helper()

	labelID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		document, err := contentBlocks.CreateDocument(t.Context(), tx, contentblock.CreateInput{
			Profile: creativeContentProfile, SourceLocale: "en",
		})
		if err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO label (id, status, content_document_id, created_at, updated_at)
			VALUES (?, 'LABEL_STATUS_DRAFT', ?, ?, ?)
		`, labelID, document.Document.ID, now, now).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO label_owner (label_id, member_id, created_at)
			VALUES (?, ?, ?)
		`, labelID, ownerID, now).Error
	}))
	return labelID
}

func TestInternalLabelSourceNameSaveQueuesOgGeneration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	identityID := uuid.NewString()
	seedExternalKratosIdentityWithTraits(t, db, identityID, "Label source name admin")
	requireInternalResourceAdmin(t, stack.SpiceDBClient, identityID)
	memberCtx := labelTargetMemberContext(t, identityID)
	contentBlocks := newCreativeContentIntegrationStore(t, stack.SpiceDBClient)
	labelID := seedInternalLabel(t, db, contentBlocks, integrationMemberID(identityID))
	attachInternalResourcePolicy(t, stack.SpiceDBClient, labelID)
	logoFileID := seedImageBindingUploadedFileFixtureForKind(t, db, "label/"+labelID+"/source-name-save.webp", "logo")
	var logoAssetID string
	require.NoError(t, db.Table("public_asset").
		Select("id").Where("source_file_id = ?", logoFileID).Scan(&logoAssetID).Error)
	require.NoError(t, mediaasset.NewLifecycle(db, "").BindPublicAsset(context.Background(), mediaasset.Binding{
		AssetID: logoAssetID, OwnerType: "label", OwnerID: labelID, BindingKey: "logo:light", SourceFileID: &logoFileID,
	}))
	require.NoError(t, db.Model(&model.Label{}).Where("id = ?", labelID).
		Update("logo_light_file_id", logoFileID).Error)
	now := time.Now().UTC()
	initialName := "Initial Label"
	require.NoError(t, saveLabelSourceLocaleDocumentState(
		context.Background(), db, labelID, "en", translationLocaleDocumentSaveInput{
			Title: &initialName, OverwriteNullFields: true, Now: now,
		},
	))
	revision := attachCreativeContentIntegrationDocument(
		t, db, contentBlocks, labelContentEntity, labelID, "en", "Initial description",
	)
	require.NoError(t, db.Exec(`INSERT INTO label_translation (
		entity_id, locale, title, created_at, updated_at
	) VALUES (?, 'ko', 'Target title', ?, ?)`,
		labelID, now, now,
	).Error)
	jobID := uuid.NewString()
	requestXLIFF := []byte(`<?xml version="1.0" encoding="UTF-8"?><xliff xmlns="urn:oasis:names:tc:xliff:document:2.2" version="2.2" srcLang="en" trgLang="ko"></xliff>`)
	require.NoError(t, db.Exec(`INSERT INTO translation_job (
		id, entity_type, entity_id, target_locale, source_locale, request_artifact_digest,
		operation_id, status, request_xliff, request_manifest, requested_by_member_id,
		requested_at, created_at, updated_at
	) VALUES (?, 'label', ?, 'ko', 'en', ?, ?, 'queued', ?, '{}'::jsonb, ?, ?, ?, ?)`,
		jobID, labelID, strings.Repeat("0", 64),
		uuid.NewString(), requestXLIFF, integrationMemberID(identityID), now, now, now,
	).Error)
	var targetBefore struct{ Title string }
	require.NoError(t, db.Table("label_translation").Where("entity_id = ? AND locale = 'ko'", labelID).Take(&targetBefore).Error)
	type jobSnapshot struct {
		ID                    string
		TargetLocale          string
		SourceLocale          string
		RequestArtifactDigest string
		OperationID           string
		Status                string
		CancelRequested       bool
		RequestedAt           time.Time
		UpdatedAt             time.Time
	}
	loadJobs := func() []jobSnapshot {
		var jobs []jobSnapshot
		require.NoError(t, db.Table("translation_job").
			Where("entity_type = 'label' AND entity_id = ?", labelID).
			Order("id").Find(&jobs).Error)
		return jobs
	}
	jobsBefore := loadJobs()
	require.Len(t, jobsBefore, 1)
	require.Equal(t, jobID, jobsBefore[0].ID)
	service := NewAuditedInternalLabelService(
		db, &capturingAsyncPublisher{}, stack.SpiceDBClient, apitelemetry.NewDurableWriter(db),
		Dependencies{Translation: labeladapter.NewTranslation(), Runtime: newLabelRuntimeForTest(db, "")},
		WithInternalLabelContentBlockStore(contentBlocks),
		WithInternalLabelCheckpoints(testcollaboration.NewCheckpoints(db, stack.SpiceDBClient)),
	)
	name := "Renamed Label"
	var contributorID string
	require.NoError(t, db.Table("label_owner").Select("member_id::text").Where("label_id = ?", labelID).Scan(&contributorID).Error)

	response, err := service.UpdateLabelLocaleMetadata(memberCtx, connect.NewRequest(&intrav1.UpdateLabelLocaleMetadataRequest{
		LabelId:              labelID,
		Locale:               "en",
		Title:                &name,
		ExpectedRevision:     revision,
		ContributorMemberIds: []string{contributorID},
	}))
	require.NoError(t, err)
	require.True(t, response.Msg.Changed)
	var targetAfter struct{ Title string }
	require.NoError(t, db.Table("label_translation").Where("entity_id = ? AND locale = 'ko'", labelID).Take(&targetAfter).Error)
	require.Equal(t, targetBefore, targetAfter)
	require.Equal(t, jobsBefore, loadJobs())

	var targetCount int64
	require.NoError(t, db.Model(&model.OgGenerationTarget{}).
		Where("entity_type = ? AND entity_id = ?", "label", labelID).
		Count(&targetCount).Error)
	require.EqualValues(t, 1, targetCount)
}
