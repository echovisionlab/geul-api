//go:build integration

package legal_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	legalpostgres "github.com/echovisionlab/geul-api/internal/legal/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// archivedLegalOps runs the reads and collaboration writes of one policy
// kind.
type archivedLegalOps struct {
	create   func(context.Context, legalIntegration) (string, string, error)
	get      func(context.Context, legalIntegration, string) error
	load     func(context.Context, legalIntegration, string, string) (string, error)
	apply    func(context.Context, legalIntegration, string, *contentv1.RichTextBlockMutationBatch) (string, error)
	metadata func(context.Context, legalIntegration, string, string, string, []string) error
}

var archivedLegalTermsOps = archivedLegalOps{
	create: func(ctx context.Context, l legalIntegration) (string, string, error) {
		response, err := l.terms().CreateTermsVersion(ctx, connect.NewRequest(&managev1.CreateTermsVersionRequest{
			Title: ptrString("Archived terms"), Document: legalPolicyDocumentFixture("en", "Archived terms"),
		}))
		if err != nil {
			return "", "", err
		}
		return response.Msg.Id, response.Msg.Revision, nil
	},
	get: func(ctx context.Context, l legalIntegration, id string) error {
		_, err := l.terms().GetTermsVersion(ctx, connect.NewRequest(&managev1.GetTermsVersionRequest{Id: id}))
		return err
	},
	load: func(ctx context.Context, l legalIntegration, id, sessionID string) (string, error) {
		response, err := l.internalTerms().LoadTermsBlockDocument(ctx, connect.NewRequest(&intrav1.LoadTermsBlockDocumentRequest{
			TermsId: id, Locale: "en", Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
		}))
		if err != nil {
			return "", err
		}
		return response.Msg.DocumentRevision, nil
	},
	apply: func(ctx context.Context, l legalIntegration, id string, batch *contentv1.RichTextBlockMutationBatch) (string, error) {
		response, err := l.internalTerms().ApplyTermsBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyTermsBlockBatchRequest{TermsId: id, Locale: "en", Batch: batch}))
		if err != nil {
			return "", err
		}
		return response.Msg.DocumentRevision, nil
	},
	metadata: func(ctx context.Context, l legalIntegration, id, revision, title string, contributors []string) error {
		_, err := l.internalTerms().UpdateTermsLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdateTermsLocaleMetadataRequest{
			TermsId: id, Locale: "en", ExpectedRevision: revision, Title: &title, ContributorMemberIds: contributors,
		}))
		return err
	},
}

var archivedLegalPrivacyOps = archivedLegalOps{
	create: func(ctx context.Context, l legalIntegration) (string, string, error) {
		response, err := l.privacy().CreatePrivacyVersion(ctx, connect.NewRequest(&managev1.CreatePrivacyVersionRequest{
			Title: ptrString("Archived privacy"), Document: legalPolicyDocumentFixture("en", "Archived privacy"),
		}))
		if err != nil {
			return "", "", err
		}
		return response.Msg.Id, response.Msg.Revision, nil
	},
	get: func(ctx context.Context, l legalIntegration, id string) error {
		_, err := l.privacy().GetPrivacyVersion(ctx, connect.NewRequest(&managev1.GetPrivacyVersionRequest{Id: id}))
		return err
	},
	load: func(ctx context.Context, l legalIntegration, id, sessionID string) (string, error) {
		response, err := l.internalPrivacy().LoadPrivacyBlockDocument(ctx, connect.NewRequest(&intrav1.LoadPrivacyBlockDocumentRequest{
			PrivacyId: id, Locale: "en", Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
		}))
		if err != nil {
			return "", err
		}
		return response.Msg.DocumentRevision, nil
	},
	apply: func(ctx context.Context, l legalIntegration, id string, batch *contentv1.RichTextBlockMutationBatch) (string, error) {
		response, err := l.internalPrivacy().ApplyPrivacyBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyPrivacyBlockBatchRequest{PrivacyId: id, Locale: "en", Batch: batch}))
		if err != nil {
			return "", err
		}
		return response.Msg.DocumentRevision, nil
	},
	metadata: func(ctx context.Context, l legalIntegration, id, revision, title string, contributors []string) error {
		_, err := l.internalPrivacy().UpdatePrivacyLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdatePrivacyLocaleMetadataRequest{
			PrivacyId: id, Locale: "en", ExpectedRevision: revision, Title: &title, ContributorMemberIds: contributors,
		}))
		return err
	},
}

// TestArchivedLegalPoliciesAllowOnlyAdminSourceEditingIntegration pins the
// SpiceDB schema answers for an archived version: a global Author may view,
// open and export it (view_archived) but not edit it (edit_archived), which
// an Admin holds. The use cases' choice of action is unit-tested; this test
// keeps the schema and the collaboration session in the loop.
func TestArchivedLegalPoliciesAllowOnlyAdminSourceEditingIntegration(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		kind     legaldomain.PolicyKind
		archived string
		ops      archivedLegalOps
	}{
		{kind: legaldomain.Terms, archived: managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String(), ops: archivedLegalTermsOps},
		{kind: legaldomain.Privacy, archived: managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String(), ops: archivedLegalPrivacyOps},
	} {
		t.Run(string(testCase.kind), func(t *testing.T) {
			db := newLegalLocalKratosIntegrationDB(t)
			adminCtx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
			admin := auth.GetUser(adminCtx)
			require.NotNil(t, admin)
			integration := newLegalIntegration(t, db, spiceDB, &audittest.Recorder{})
			ops := testCase.ops

			authorID := testutil.IntegrationUUID()
			authorMemberID := testutil.SeedPostIntegrationIdentity(t, db, authorID, "Archived legal Author")
			testutil.GrantIntegrationGlobalRole(t, spiceDB, authorID, policyv1.Role.Author())
			authorSessionID := insertArchivedLegalIntegrationSession(t, db, authorID)
			authorCtx := auth.WithUser(context.Background(), &auth.UserInfo{
				IdentityID: auth.IdentityID(authorID), MemberID: auth.MemberID(authorMemberID),
				SessionID: auth.SessionID(authorSessionID), Authenticated: true, Onboarded: true,
			})

			id, revision, err := ops.create(adminCtx, integration)
			require.NoError(t, err)
			require.NoError(t, db.Table(string(testCase.kind)+"_history").Where("id = ?", id).Update("status", testCase.archived).Error)
			require.NoError(t, ops.get(authorCtx, integration, id), "a global Author may read an archived legal document for the editor route")

			loadedRevision, err := ops.load(context.Background(), integration, id, authorSessionID)
			require.NoError(t, err, "a global Author may open an archived legal collaboration document read-only")
			require.Equal(t, revision, loadedRevision)

			_, err = ops.apply(authorCtx, integration, id, legalArchivedParagraphMutationBatch(loadedRevision, authorMemberID))
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
			err = ops.metadata(authorCtx, integration, id, loadedRevision, "Author must not edit archived source metadata", []string{authorMemberID})
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

			revision, err = ops.apply(adminCtx, integration, id, legalArchivedParagraphMutationBatch(loadedRevision, admin.MemberID.String()))
			require.NoError(t, err)
			require.NoError(t, ops.metadata(adminCtx, integration, id, revision, "Admin archived metadata", []string{admin.MemberID.String()}))

			store := legalpostgres.Bind(db, legalpostgres.Ports{}, nil).Store
			lockActive := func(ctx context.Context) legaldomain.LockActive {
				return func(principal *auth.UserInfo) (bool, error) { return identitystate.LockActivePrincipal(ctx, db, principal) }
			}
			require.NoError(t, legaldomain.RequireTranslationInterchangeView(authorCtx, store, spiceDB, lockActive(authorCtx), string(testCase.kind), id),
				"a global Author may export an archived legal translation read-only")
			err = legaldomain.RequireEditableTranslationMutation(authorCtx, store, spiceDB, lockActive(authorCtx), string(testCase.kind), id)
			require.Equal(t, connect.CodePermissionDenied, errs.CodeOf(err))
			require.NoError(t, legaldomain.RequireEditableTranslationMutation(adminCtx, store, spiceDB, lockActive(adminCtx), string(testCase.kind), id))
		})
	}
}

func insertArchivedLegalIntegrationSession(t *testing.T, db *gorm.DB, identityID string) string {
	t.Helper()
	sessionID := testutil.IntegrationUUID()
	require.NoError(t, db.Exec(`
		INSERT INTO kratos.sessions (
			id, identity_id, active, authenticated_at, expires_at,
			created_at, updated_at, authentication_methods
		) VALUES (
			?::uuid, ?::uuid, TRUE, NOW(), NOW() + INTERVAL '1 hour',
			NOW(), NOW(), '[]'::jsonb
		)
	`, sessionID, identityID).Error)
	return sessionID
}

func legalArchivedParagraphMutationBatch(expectedRevision, contributorMemberID string) *contentv1.RichTextBlockMutationBatch {
	blockID := testutil.IntegrationUUID()
	return &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY,
		ExpectedRevision:        expectedRevision,
		ContributorMemberIds:    []string{contributorMemberID},
		BaseMutations: []*contentv1.RichTextBlockMutation{{
			Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
				Node: &contentv1.RichTextBlockNode{
					Block: &contentv1.RichTextBlock{
						Id: blockID,
						Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{
							Props: &contentv1.ParagraphProps{},
						}},
					},
					Placement: &contentv1.ContentBlockPlacement{Index: 1},
				},
			}},
		}},
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: "en",
			Mutations: []*contentv1.RichTextBlockLocaleMutation{{
				Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{
					Block: &contentv1.RichTextBlockLocale{
						BlockId: blockID,
						Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
							Props: &contentv1.ParagraphLocaleProps{},
							Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
								Text: &contentv1.RichTextStyledText{Text: "Archived legal source edit"},
							}}},
						}},
					},
				}},
			}},
		}},
	}
}
