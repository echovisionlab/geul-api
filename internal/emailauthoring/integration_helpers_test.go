//go:build integration

package emailauthoring

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

type integrationCampaignDeliveryReferences struct{}

func (integrationCampaignDeliveryReferences) TemplateDeliveryRunCounts(ctx context.Context, db *gorm.DB, ids []string) (map[string]int32, error) {
	return integrationReferenceCounts(ctx, db, "email_delivery_run", "source_template_id", ids)
}

func (integrationCampaignDeliveryReferences) LayoutExternalReferenceCounts(ctx context.Context, db *gorm.DB, ids []string) (map[string]LayoutExternalReferenceCounts, error) {
	campaigns, err := integrationReferenceCounts(ctx, db, "campaign", "layout_id", ids)
	if err != nil {
		return nil, err
	}
	runs, err := integrationReferenceCounts(ctx, db, "email_delivery_run", "source_layout_id", ids)
	if err != nil {
		return nil, err
	}
	result := make(map[string]LayoutExternalReferenceCounts, len(ids))
	for _, id := range ids {
		result[id] = LayoutExternalReferenceCounts{Campaigns: campaigns[id], DeliveryRuns: runs[id]}
	}
	return result, nil
}

func (integrationCampaignDeliveryReferences) RequireTemplateMutable(ctx context.Context, db *gorm.DB, id string) error {
	return integrationRequireNoActiveDelivery(ctx, db, "source_template_id", id)
}

func (integrationCampaignDeliveryReferences) RequireLayoutMutable(ctx context.Context, db *gorm.DB, id string) error {
	return integrationRequireNoActiveDelivery(ctx, db, "source_layout_id", id)
}

func (integrationCampaignDeliveryReferences) DetachTemplateHistory(ctx context.Context, db *gorm.DB, id string) error {
	return integrationDetachDeliveryHistory(ctx, db, "source_template_id", id)
}

func (integrationCampaignDeliveryReferences) DetachLayoutHistory(ctx context.Context, db *gorm.DB, id string) error {
	return integrationDetachDeliveryHistory(ctx, db, "source_layout_id", id)
}

func integrationReferenceCounts(ctx context.Context, db *gorm.DB, table, column string, ids []string) (map[string]int32, error) {
	var rows []struct {
		ID    string
		Count int64
	}
	if err := db.WithContext(ctx).Table(table).Select(column+" AS id, COUNT(*) AS count").Where(column+" IN ?", ids).Group(column).Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]int32, len(rows))
	for _, row := range rows {
		result[row.ID] = int32(row.Count)
	}
	return result, nil
}

func integrationRequireNoActiveDelivery(ctx context.Context, db *gorm.DB, column, id string) error {
	var count int64
	if err := db.WithContext(ctx).Table("email_delivery_run").Where(column+" = ? AND status IN ?", id, []string{"scheduled", "sending"}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return errs.FailedPrecondition("Email Authoring resource is frozen by an active delivery run")
	}
	return nil
}

func integrationDetachDeliveryHistory(ctx context.Context, db *gorm.DB, column, id string) error {
	return db.WithContext(ctx).Table("email_delivery_run").Where(column+" = ? AND status NOT IN ?", id, []string{"scheduled", "sending"}).Update(column, nil).Error
}

func publishEmailTemplateSourceBlocksForIntegration(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient, templateID, text string) {
	t.Helper()
	store := testutil.NewEmailContentBlockStore(t, spiceDB)
	documentID, err := loadCampaignEmailContentDocumentID(t.Context(), db, emailTemplateContentEntity, templateID)
	require.NoError(t, err)
	domain, err := loadCampaignEmailSourceContext(t.Context(), db, emailTemplateContentEntity, templateID)
	require.NoError(t, err)
	snapshot, err := store.LoadSnapshot(t.Context(), db, documentID, domain.SourceLocale)
	require.NoError(t, err)
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	require.NoError(t, err)
	contributorID := testutil.IntegrationUUID()
	testutil.InsertAuthorizedDocumentContributor(t, db, spiceDB, contributorID)
	_, err = NewAuditedInternalEmailTemplateService(
		db,
		apitelemetry.NewDurableWriter(db),
		spiceDB,
		WithInternalEmailTemplateContentBlockStore(store),
		WithInternalEmailTemplateCheckpoints(testcollaboration.NewCheckpoints(db, spiceDB)),
		WithInternalEmailTemplateCampaignDeliveryReferences(integrationCampaignDeliveryReferences{}),
	).ApplyBlockBatch(t.Context(), connect.NewRequest(&intrav1.ApplyEmailTemplateBlockBatchRequest{
		EmailTemplateId: templateID,
		Locale:          domain.SourceLocale,
		Batch:           testutil.NewParagraphBatch(document, snapshot.Document.Revision.String(), domain.SourceLocale, text, []string{contributorID}),
	}))
	require.NoError(t, err)
}
