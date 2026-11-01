package campaign_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/emailcontent/emailcontenttest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAcceptedTranslationReplacesCurrentTargetWhileInterchangeKeepsItsCAS(t *testing.T) {
	h := newHarness(t)
	row := h.store.PutCampaign(campaign.Campaign{})
	manual := "Manual target"
	h.store.PutLocale(row.ID, emailcontenttest.Locale{LocaleMetadata: emailcontent.LocaleMetadata{Locale: "ko", Subject: &manual, UpdatedAt: h.now}})
	old, err := campaign.LoadTranslationInterchangeTarget(h.ctx, h.joined(h.store), row.ID, "ko")
	require.NoError(t, err)
	source, err := campaign.LoadTranslationSourceDocument(h.ctx, h.joined(h.store), row.ID)
	require.NoError(t, err)
	require.Equal(t, row.Subject, source.Title)
	newer := "Newer manual target"
	h.store.PutLocale(row.ID, emailcontenttest.Locale{LocaleMetadata: emailcontent.LocaleMetadata{Locale: "ko", Subject: &newer, UpdatedAt: h.now.Add(time.Second)}})
	interchangeErr := h.transaction(func(ctx context.Context, j campaign.Joined) error {
		_, err := campaign.ApplyTranslationInterchange(ctx, j, row.ID, "en", emailcontent.InterchangeMutation{TargetLocale: "ko", ExpectedDocumentRevision: source.ContentDocumentRevision, ExpectedTargetRevision: &old.Revision, ExpectedPresence: true, Subject: &manual, ContributorMemberID: auth.GetUser(h.ctx).MemberID.String(), Now: h.now.Add(2 * time.Second)})
		return err
	})
	var conflict *translation.TargetRevisionConflict
	require.True(t, errors.As(interchangeErr, &conflict))
	stillManual, _ := h.store.Locale(row.ID, "ko")
	require.Equal(t, newer, *stillManual.Subject)
	frozen := row
	frozen.Status = campaign.CampaignStatusSent
	h.store.PutCampaign(frozen)
	ai := "AI target"
	candidate := &translation.Candidate{Title: &ai, ContentDocumentRevision: uuid.NewString(), ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"}}
	job := &emailcontent.TranslationJob{EntityType: "campaign", EntityID: row.ID, SourceLocale: "en", TargetLocale: "ko", RequestedByMemberID: auth.GetUser(h.ctx).MemberID.String()}
	require.NoError(t, h.transaction(func(ctx context.Context, j campaign.Joined) error {
		return campaign.ApplyTranslationCandidate(ctx, j, job, candidate, translation.EntryWrite{Title: &ai, Now: h.now.Add(3 * time.Second)})
	}))
	target, exists := h.store.Locale(row.ID, "ko")
	require.True(t, exists)
	require.Equal(t, ai, *target.Subject)
	after, err := campaign.LoadTranslationSourceDocument(h.ctx, h.joined(h.store), row.ID)
	require.NoError(t, err)
	require.Equal(t, source.ContentDocumentRevision, after.ContentDocumentRevision)
	require.Equal(t, source.Title, after.Title)
	require.Len(t, h.audit.Records(), 1)
}
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
