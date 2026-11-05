package campaign_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/emailcontent/emailcontenttest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSharedPresentationChangeProjectsSourceWithoutRewritingTargets(t *testing.T) {
	h := newHarness(t)
	row := h.store.PutCampaign(campaign.Campaign{})
	document, _ := h.store.Document(uuid.MustParse(*row.ContentDocumentID))
	stale := "old source"
	subject := row.Subject
	h.store.PutLocale(row.ID, emailcontenttest.Locale{LocaleMetadata: emailcontent.LocaleMetadata{Locale: "en", Subject: &subject, UpdatedAt: h.now}, HTML: &stale})
	target := "target HTML"
	h.store.PutLocale(row.ID, emailcontenttest.Locale{LocaleMetadata: emailcontent.LocaleMetadata{Locale: "ko", Subject: &subject, UpdatedAt: h.now}, HTML: &target})
	h.store.NextResult(contentblock.Result{Changed: true, ContentChanged: true, TranslationSourceChanged: false})
	changed, err := h.internal.ApplyCampaignBlockBatch(h.ctx, campaign.ApplyCampaignBlockBatchCommand{CampaignID: row.ID, Locale: "en", Batch: &contentv1.RichTextBlockMutationBatch{BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Profile: contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL, ExpectedRevision: document.Revision.String(), ContributorMemberIds: []string{auth.GetUser(h.ctx).MemberID.String()}, BaseMutations: []*contentv1.RichTextBlockMutation{{Operation: &contentv1.RichTextBlockMutation_Move{Move: &contentv1.MoveRichTextBlock{BlockId: uuid.NewString(), Placement: &contentv1.ContentBlockPlacement{}}}}}}})
	require.NoError(t, err)
	require.True(t, changed.Changed)
	require.False(t, changed.SourceChanged)
	require.Empty(t, changed.ChangedLocales)
	source, _ := h.store.Locale(row.ID, "en")
	require.NotNil(t, source.HTML)
	require.NotEqual(t, stale, *source.HTML)
	locale, _ := h.store.Locale(row.ID, "ko")
	require.Equal(t, target, *locale.HTML)
	require.Equal(t, h.now, locale.UpdatedAt)
	require.Equal(t, []mutation.Step{mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepCommit}, h.units.Steps())
}
func TestDocumentLoadResolvesSessionAndRequiresAnEditableCampaign(t *testing.T) {
	h := newHarness(t)
	row := h.store.PutCampaign(campaign.Campaign{})
	principal := &campaign.CollaborationPrincipal{SessionID: auth.GetUser(h.ctx).SessionID.String()}
	loaded, err := h.internal.LoadCampaignDocument(h.ctx, campaign.LoadCampaignDocumentQuery{CampaignID: row.ID, Locale: "en", Principal: principal})
	require.NoError(t, err)
	require.Equal(t, "en", loaded.Locale)
	require.NotNil(t, loaded.Document)
	h.checker.Deny(policyv1.Campaign.Edit(row.ID))
	_, err = h.internal.LoadCampaignDocument(h.ctx, campaign.LoadCampaignDocumentQuery{CampaignID: row.ID, Locale: "en", Principal: principal})
	require.Equal(t, connect.CodePermissionDenied, code(err))
	_, err = h.internal.LoadCampaignDocument(h.ctx, campaign.LoadCampaignDocumentQuery{CampaignID: row.ID, Locale: "en", Principal: &campaign.CollaborationPrincipal{SessionID: "invalid"}})
	require.Equal(t, connect.CodeUnauthenticated, code(err))
}
