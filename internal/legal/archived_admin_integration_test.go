//go:build integration

package legal_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/proto"
)

func TestArchivedLegalPoliciesAllowOnlyAdminSourceEditingIntegration(t *testing.T) {
	type legalPolicyCase struct {
		name      string
		table     string
		entity    managev1.TranslationEntityType
		archived  string
		scheduled string
		active    string
		create    func(context.Context, *gorm.DB, *contentblock.Store, *auth.SpiceDBClient) (string, string, error)
		get       func(context.Context, *gorm.DB, string, *contentblock.Store, *auth.SpiceDBClient) (string, error)
		load      func(context.Context, *gorm.DB, string, string, *contentblock.Store, *auth.SpiceDBClient) (string, error)
		apply     func(context.Context, *gorm.DB, string, *contentv1.RichTextBlockMutationBatch, *contentblock.Store, *auth.SpiceDBClient) (string, error)
		metadata  func(context.Context, *gorm.DB, string, string, string, []string, *contentblock.Store, *auth.SpiceDBClient) (string, error)
	}

	for _, testCase := range []legalPolicyCase{
		{
			name: "terms", table: "terms_history", entity: managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_TERMS,
			archived:  managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String(),
			scheduled: managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(),
			active:    managev1.TermsStatus_TERMS_STATUS_ACTIVE.String(),
			create: func(ctx context.Context, db *gorm.DB, store *contentblock.Store, spiceDB *auth.SpiceDBClient) (string, string, error) {
				response, err := newTermsServiceForLegalIntegrationTest(db, "", "", spiceDB, legaldomain.WithTermsContentBlockStore(store)).CreateTermsVersion(
					ctx, connect.NewRequest(&managev1.CreateTermsVersionRequest{Title: ptrString("Archived terms"), Document: legalPolicyDocumentFixture("en", "Archived terms")}),
				)
				if err != nil {
					return "", "", err
				}
				return response.Msg.Id, response.Msg.Revision, nil
			},
			get: func(ctx context.Context, db *gorm.DB, id string, store *contentblock.Store, spiceDB *auth.SpiceDBClient) (string, error) {
				response, err := newTermsServiceForLegalIntegrationTest(db, "", "", spiceDB, legaldomain.WithTermsContentBlockStore(store)).GetTermsVersion(
					ctx, connect.NewRequest(&managev1.GetTermsVersionRequest{Id: id}),
				)
				if err != nil {
					return "", err
				}
				return response.Msg.Id, nil
			},
			load: func(ctx context.Context, db *gorm.DB, id, sessionID string, store *contentblock.Store, spiceDB *auth.SpiceDBClient) (string, error) {
				service := legaldomain.NewInternalTermsService(db, legalIntegrationDependencies(db, ""))
				legaldomain.WithInternalTermsContentBlocks(store, spiceDB, testcollaboration.NewCheckpoints(db, spiceDB))(service)
				response, err := service.LoadTermsBlockDocument(ctx, connect.NewRequest(&intrav1.LoadTermsBlockDocumentRequest{
					TermsId: id, Locale: "en", Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
				}))
				if err != nil {
					return "", err
				}
				return response.Msg.DocumentRevision, nil
			},
			apply: func(ctx context.Context, db *gorm.DB, id string, batch *contentv1.RichTextBlockMutationBatch, store *contentblock.Store, spiceDB *auth.SpiceDBClient) (string, error) {
				service := legaldomain.NewInternalTermsService(db, legalIntegrationDependencies(db, ""))
				legaldomain.WithInternalTermsContentBlocks(store, spiceDB, testcollaboration.NewCheckpoints(db, spiceDB))(service)
				response, err := service.ApplyTermsBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyTermsBlockBatchRequest{TermsId: id, Locale: "en", Batch: batch}))
				if err != nil {
					return "", err
				}
				return response.Msg.DocumentRevision, nil
			},
			metadata: func(ctx context.Context, db *gorm.DB, id, revision, title string, contributors []string, store *contentblock.Store, spiceDB *auth.SpiceDBClient) (string, error) {
				service := legaldomain.NewInternalTermsService(db, legalIntegrationDependencies(db, ""))
				legaldomain.WithInternalTermsContentBlocks(store, spiceDB, testcollaboration.NewCheckpoints(db, spiceDB))(service)
				response, err := service.UpdateTermsLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdateTermsLocaleMetadataRequest{
					TermsId: id, Locale: "en", ExpectedRevision: revision, Title: &title, ContributorMemberIds: contributors,
				}))
				if err != nil {
					return "", err
				}
				return response.Msg.DocumentRevision, nil
			},
		},
		{
			name: "privacy", table: "privacy_history", entity: managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_PRIVACY,
			archived:  managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String(),
			scheduled: managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(),
			active:    managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String(),
			create: func(ctx context.Context, db *gorm.DB, store *contentblock.Store, spiceDB *auth.SpiceDBClient) (string, string, error) {
				response, err := newPrivacyServiceForLegalIntegrationTest(db, "", "", spiceDB, legaldomain.WithPrivacyContentBlockStore(store)).CreatePrivacyVersion(
					ctx, connect.NewRequest(&managev1.CreatePrivacyVersionRequest{Title: ptrString("Archived privacy"), Document: legalPolicyDocumentFixture("en", "Archived privacy")}),
				)
				if err != nil {
					return "", "", err
				}
				return response.Msg.Id, response.Msg.Revision, nil
			},
			get: func(ctx context.Context, db *gorm.DB, id string, store *contentblock.Store, spiceDB *auth.SpiceDBClient) (string, error) {
				response, err := newPrivacyServiceForLegalIntegrationTest(db, "", "", spiceDB, legaldomain.WithPrivacyContentBlockStore(store)).GetPrivacyVersion(
					ctx, connect.NewRequest(&managev1.GetPrivacyVersionRequest{Id: id}),
				)
				if err != nil {
					return "", err
				}
				return response.Msg.Id, nil
			},
			load: func(ctx context.Context, db *gorm.DB, id, sessionID string, store *contentblock.Store, spiceDB *auth.SpiceDBClient) (string, error) {
				service := legaldomain.NewInternalPrivacyService(db, legalIntegrationDependencies(db, ""))
				legaldomain.WithInternalPrivacyContentBlocks(store, spiceDB, testcollaboration.NewCheckpoints(db, spiceDB))(service)
				response, err := service.LoadPrivacyBlockDocument(ctx, connect.NewRequest(&intrav1.LoadPrivacyBlockDocumentRequest{
					PrivacyId: id, Locale: "en", Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
				}))
				if err != nil {
					return "", err
				}
				return response.Msg.DocumentRevision, nil
			},
			apply: func(ctx context.Context, db *gorm.DB, id string, batch *contentv1.RichTextBlockMutationBatch, store *contentblock.Store, spiceDB *auth.SpiceDBClient) (string, error) {
				service := legaldomain.NewInternalPrivacyService(db, legalIntegrationDependencies(db, ""))
				legaldomain.WithInternalPrivacyContentBlocks(store, spiceDB, testcollaboration.NewCheckpoints(db, spiceDB))(service)
				response, err := service.ApplyPrivacyBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyPrivacyBlockBatchRequest{PrivacyId: id, Locale: "en", Batch: batch}))
				if err != nil {
					return "", err
				}
				return response.Msg.DocumentRevision, nil
			},
			metadata: func(ctx context.Context, db *gorm.DB, id, revision, title string, contributors []string, store *contentblock.Store, spiceDB *auth.SpiceDBClient) (string, error) {
				service := legaldomain.NewInternalPrivacyService(db, legalIntegrationDependencies(db, ""))
				legaldomain.WithInternalPrivacyContentBlocks(store, spiceDB, testcollaboration.NewCheckpoints(db, spiceDB))(service)
				response, err := service.UpdatePrivacyLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdatePrivacyLocaleMetadataRequest{
					PrivacyId: id, Locale: "en", ExpectedRevision: revision, Title: &title, ContributorMemberIds: contributors,
				}))
				if err != nil {
					return "", err
				}
				return response.Msg.DocumentRevision, nil
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newLegalIntegrationDB(t)
			adminCtx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
			admin := auth.GetUser(adminCtx)
			require.NotNil(t, admin)
			store := newLegalLifecycleContentBlockStore(t, spiceDB)

			authorID := integrationTestUUID()
			authorMemberID := seedExternalKratosIdentityWithTraits(t, db, authorID, "Archived legal Author")
			grantIntegrationGlobalRole(t, spiceDB, authorID, policyv1.Role.Author())
			authorSessionID := insertArchivedLegalIntegrationSession(t, db, authorID)
			authorCtx := auth.WithUser(context.Background(), &auth.UserInfo{
				IdentityID: auth.IdentityID(authorID), MemberID: auth.MemberID(authorMemberID),
				SessionID: auth.SessionID(authorSessionID), Authenticated: true, Onboarded: true,
			})

			id, revision, err := testCase.create(adminCtx, db, store, spiceDB)
			require.NoError(t, err)
			require.NoError(t, db.Table(testCase.table).Where("id = ?", id).Update("status", testCase.archived).Error)
			viewedID, err := testCase.get(authorCtx, db, id, store, spiceDB)
			require.NoError(t, err, "a global Author may read an archived legal document for the editor route")
			require.Equal(t, id, viewedID)

			loadedRevision, err := testCase.load(context.Background(), db, id, authorSessionID, store, spiceDB)
			require.NoError(t, err, "a global Author may open an archived legal collaboration document read-only")
			require.Equal(t, revision, loadedRevision)

			_, err = testCase.apply(authorCtx, db, id, legalArchivedParagraphMutationBatch(loadedRevision, integrationMemberID(authorID)), store, spiceDB)
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
			_, err = testCase.metadata(authorCtx, db, id, loadedRevision, "Author must not edit archived source metadata", []string{integrationMemberID(authorID)}, store, spiceDB)
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

			revision, err = testCase.apply(adminCtx, db, id, legalArchivedParagraphMutationBatch(loadedRevision, admin.MemberID.String()), store, spiceDB)
			require.NoError(t, err)
			revision, err = testCase.metadata(adminCtx, db, id, revision, "Admin archived metadata", []string{admin.MemberID.String()}, store, spiceDB)
			require.NoError(t, err)

			err = legaldomain.RequireTranslationInterchangeViewWithDB(
				authorCtx, db, spiceDB, testCase.name, id,
			)
			require.NoError(t, err, "a global Author may export an archived legal translation read-only")
			err = legaldomain.RequireEditableTranslationMutationWithDB(
				authorCtx, db, spiceDB, testCase.name, id,
			)
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
			var rootLocale struct {
				Locale string `gorm:"column:source_locale"`
			}
			require.NoError(t, db.Table(testCase.table).Select("source_locale").Where("id = ?", id).Take(&rootLocale).Error)
			err = legaldomain.RequireEditableTranslationMutationWithDB(
				adminCtx, db, spiceDB, testCase.name, id,
			)
			require.NoError(t, err)
			err = legaldomain.RequireTranslationInterchangeViewWithDB(
				adminCtx, db, spiceDB, testCase.name, id,
			)
			require.NoError(t, err, "an Admin may export an archived legal translation")
			source, err := legaldomain.LoadTypedTranslationSourceDocument(
				context.Background(), db, store, testCase.name, id,
			)
			require.NoError(t, err)
			require.Equal(t, rootLocale.Locale, source.SourceLocale)
			providerDocumentRevision := source.ContentDocumentRevision
			providerSourceLocale := source.SourceLocale
			targetOverlay := proto.Clone(source.ContentBlockDocument.GetLocaleOverlay()).(*contentv1.RichTextLocaleOverlay)
			targetOverlay.Locale = "ko"
			machineTitle := "Admin-requested archived " + testCase.name + " translation"
			machineText := "관리자 요청 번역"
			targetOverlay.Blocks[0].GetParagraph().Content = []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
				Text: &contentv1.RichTextStyledText{Text: machineText},
			}}}
			job := &model.TranslationJob{
				EntityType: testCase.name, EntityID: id, TargetLocale: "ko", SourceLocale: source.SourceLocale,
				RequestedByMemberID: admin.MemberID.String(),
			}
			candidate := &translation.Candidate{
				Title: &machineTitle, ContentDocumentRevision: source.ContentDocumentRevision,
				ContentBlockLocaleOverlay: targetOverlay,
			}
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				return legaldomain.ApplyTypedTranslationCandidateWithDB(context.Background(), tx, store, job, candidate, noopDomainAuditAppender{})
			}), "archived translation completion keeps the command-time Admin requester attribution")

			replacementTitle := "Admin-requested replacement"
			replacement := &translation.Candidate{
				Title: &replacementTitle, ContentDocumentRevision: source.ContentDocumentRevision,
				ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"},
			}
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				return legaldomain.ApplyTypedTranslationCandidateWithDB(
					context.Background(), tx, store, job, replacement, noopDomainAuditAppender{},
				)
			}), "provider whole replacement deletes omitted current target Blocks")
			assertLegalSourceIdentityUnchangedIntegration(
				t, db, store, testCase.name, id, providerDocumentRevision, providerSourceLocale,
			)
			documentAPI, err := legaldomain.NewAuditedAIDocumentService(
				db, store, spiceDB, legalIntegrationDependencies(db, "").OG, noopDomainAuditAppender{},
			)
			require.NoError(t, err)
			targetDocument, err := documentAPI.LoadAIDocument(adminCtx, testCase.name, id, "ko")
			require.NoError(t, err)
			require.True(t, targetDocument.LocaleExists)
			require.NotNil(t, targetDocument.TargetRevision)
			require.Zero(t, legalExactLocaleBlockCountIntegration(t, targetDocument, "ko"))

			for _, immutableStatus := range []string{testCase.scheduled, testCase.active} {
				require.NoError(t, db.Table(testCase.table).Where("id = ?", id).Update("status", immutableStatus).Error)
				_, err = testCase.get(authorCtx, db, id, store, spiceDB)
				require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
				_, err := testCase.apply(adminCtx, db, id, legalArchivedParagraphMutationBatch(revision, admin.MemberID.String()), store, spiceDB)
				require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			}
		})
	}
}

func legalExactLocaleBlockCountIntegration(
	t *testing.T,
	document legaldomain.AIDocument,
	locale string,
) int {
	t.Helper()
	materialized, err := contentv1.MaterializeRichTextDocumentStorage(
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY,
		document.SourceLocale,
		document.Rows,
	)
	require.NoError(t, err)
	for _, overlay := range materialized.GetLocaleOverlays() {
		if overlay.GetLocale() == locale {
			return len(overlay.GetBlocks())
		}
	}
	return 0
}

func insertArchivedLegalIntegrationSession(t *testing.T, db *gorm.DB, identityID string) string {
	t.Helper()
	sessionID := integrationTestUUID()
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
	blockID := integrationTestUUID()
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
