//go:build integration

package integration

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	authzedv1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"google.golang.org/grpc"

	"connectrpc.com/connect"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestWorkAIDocumentTargetCASIsolationAndDeleteNoBumpIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	identityID := testutil.IntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, identityID, "Work target locale CAS")
	ctx := workIntegrationAdminCtx(identityID)
	workService := newWorkIntegrationService(t, db, identityID, referenceNoopFileDeleter{})
	blockID := uuid.New()
	isPresent := true
	created, err := workService.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title: "Work target locale CAS " + testutil.IntegrationUUID(),
		Type:  managev1.WorkType_WORK_TYPE_ARTICLE, Year: 2026, Month: 8, IsPresent: &isPresent,
		Document: workTargetCASIntegrationDocument(blockID, "en", "source"),
	}))
	require.NoError(t, err)
	store, err := contentblock.NewGeneratedStore(newContentBlockFileReuseAuthorizer(integrationSpiceDB(t)))
	require.NoError(t, err)
	application := newWorkServiceForTest(t, workdomain.Deps{DB: db, ContentBlocks: store})
	contributor := testutil.PostIntegrationMemberID(identityID)

	load := func(locale string) workdomain.AIDocumentState {
		t.Helper()
		state, loadErr := application.LoadAIDocument(ctx, created.Msg.Id, locale)
		require.NoError(t, loadErr)
		return state
	}
	apply := func(state workdomain.AIDocumentState, configure func(*workdomain.AIDocumentMutation)) (workdomain.AIDocumentMutationResult, error) {
		t.Helper()
		return applyWorkAIDocument(ctx, application, workAIDocumentIntegrationMutation(state, contributor, configure))
	}

	koMissing := load("ko")
	require.False(t, koMissing.LocaleExists)
	require.Nil(t, koMissing.TargetRevision)
	require.Empty(t, koMissing.Document.GetLocaleOverlay().GetBlocks())
	sharedRevision := koMissing.DocumentRevision

	koCreated, err := apply(koMissing, func(mutation *workdomain.AIDocumentMutation) {
		mutation.CreateTranslation = true
	})
	require.NoError(t, err)
	require.Equal(t, sharedRevision, koCreated.Content.DocumentRevision.String())
	require.NotNil(t, koCreated.TargetRevision)
	ko := load("ko")
	require.True(t, ko.LocaleExists)
	require.NotNil(t, ko.TargetRevision)
	require.Len(t, ko.Document.GetLocaleOverlay().GetBlocks(), 1, "explicit create must seed every source stable unit")

	koBatch := workTargetCASIntegrationBatch(t, ko, blockID, "")
	koUpdated, err := apply(ko, func(mutation *workdomain.AIDocumentMutation) {
		mutation.Batch = koBatch
	})
	require.NoError(t, err)
	require.Equal(t, sharedRevision, koUpdated.Content.DocumentRevision.String())
	require.NotNil(t, koUpdated.TargetRevision)
	require.NotEqual(t, *ko.TargetRevision, *koUpdated.TargetRevision)
	koToken := *koUpdated.TargetRevision

	_, err = apply(ko, func(mutation *workdomain.AIDocumentMutation) {
		mutation.Batch = workTargetCASIntegrationBatch(t, ko, blockID, "stale")
	})
	var targetConflict *workdomain.AIDocumentRevisionConflictError
	require.ErrorAs(t, err, &targetConflict)
	require.Equal(t, workdomain.AIDocumentTargetRevisionConflict, targetConflict.Kind)
	require.Equal(t, koToken, *targetConflict.CurrentTargetRevision)

	frMissing := load("fr")
	frCreated, err := apply(frMissing, func(mutation *workdomain.AIDocumentMutation) {
		mutation.CreateTranslation = true
	})
	require.NoError(t, err)
	require.NotNil(t, frCreated.TargetRevision)
	fr := load("fr")
	frUpdated, err := apply(fr, func(mutation *workdomain.AIDocumentMutation) {
		mutation.Batch = workTargetCASIntegrationBatch(t, fr, blockID, "francais")
	})
	require.NoError(t, err)
	require.NotNil(t, frUpdated.TargetRevision)
	require.Equal(t, koToken, *load("ko").TargetRevision, "unrelated target locale must not invalidate ko")

	ko = load("ko")
	_, err = apply(ko, func(mutation *workdomain.AIDocumentMutation) {
		mutation.Batch.LocaleGroups = []contentblock.LocaleMutationGroup{{Locale: "ko", Deletes: []uuid.UUID{blockID}}}
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "explicit empty")

	ko = load("ko")
	koDeleted, err := apply(ko, func(mutation *workdomain.AIDocumentMutation) {
		mutation.DeleteTranslation = true
	})
	require.NoError(t, err)
	require.Equal(t, sharedRevision, koDeleted.Content.DocumentRevision.String())
	require.Nil(t, koDeleted.TargetRevision)
	require.False(t, load("ko").LocaleExists)
	require.Equal(t, *frUpdated.TargetRevision, *load("fr").TargetRevision, "target delete must not invalidate another locale")

	source := load("en")
	sourceTitle := "source changed"
	sourceUpdated, err := apply(source, func(mutation *workdomain.AIDocumentMutation) {
		mutation.Metadata = workdomain.AIDocumentMetadataPatch{SetTitle: true, Title: &sourceTitle}
	})
	require.NoError(t, err)
	require.NotEqual(t, sharedRevision, sourceUpdated.Content.DocumentRevision.String())
	require.NotEqual(t, *frUpdated.TargetRevision, *load("fr").TargetRevision, "source/shared change must invalidate target token")

	providerRevision := sourceUpdated.Content.DocumentRevision.String()
	providerJob := &model.TranslationJob{
		EntityType: "work", EntityID: created.Msg.Id, SourceLocale: "en", TargetLocale: "de",
		RequestedByMemberID: contributor,
	}
	providerTitle := "Deutsch"
	providerCandidate := &translation.Candidate{
		ContentDocumentRevision:   providerRevision,
		ContentBlockLocaleOverlay: workTargetCASIntegrationOverlay(blockID, "de", "deutsch"),
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return workdomain.ApplyTypedTranslationCandidateWithDB(
			ctx, tx, store, providerJob, providerCandidate,
			translation.EntryWrite{Title: &providerTitle, Now: time.Now().UTC()},
			apitelemetry.NewDurableWriter(db),
		)
	}))
	de := load("de")
	require.True(t, de.LocaleExists)
	require.Len(t, de.Document.GetLocaleOverlay().GetBlocks(), 1)
	require.Equal(t, providerRevision, de.DocumentRevision)

	providerReplacement := &translation.Candidate{
		ContentDocumentRevision:   providerRevision,
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "de"},
		ContentBlockLocaleDeletes: []string{blockID.String()},
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return workdomain.ApplyTypedTranslationCandidateWithDB(
			ctx, tx, store, providerJob, providerReplacement,
			translation.EntryWrite{Title: &providerTitle, Now: time.Now().UTC()},
			apitelemetry.NewDurableWriter(db),
		)
	}))
	de = load("de")
	require.True(t, de.LocaleExists, "provider replacement deletes locale values, not the translation resource")
	require.Empty(t, de.Document.GetLocaleOverlay().GetBlocks())
	require.Equal(t, providerRevision, de.DocumentRevision, "provider target replacement must not bump shared revision")
}

func workAIDocumentIntegrationMutation(
	state workdomain.AIDocumentState,
	contributor string,
	configure func(*workdomain.AIDocumentMutation),
) workdomain.AIDocumentMutation {
	mutation := workdomain.AIDocumentMutation{
		WorkID: state.Work.ID, Locale: state.Locale, ObservedSourceLocale: state.SourceLocale,
		ExpectedRevision:       state.Snapshot.Document.Revision,
		ExpectedTargetRevision: state.TargetRevision,
		ObservedLocaleExists:   state.LocaleExists, ContributorMemberID: contributor,
		Batch: contentblock.Batch{
			DocumentID: state.Snapshot.Document.ID, ExpectedRevision: state.Snapshot.Document.Revision,
			ContributorMemberIDs: []uuid.UUID{uuid.MustParse(contributor)},
		},
	}
	configure(&mutation)
	return mutation
}

func workTargetCASIntegrationBatch(
	t *testing.T,
	state workdomain.AIDocumentState,
	blockID uuid.UUID,
	text string,
) contentblock.Batch {
	t.Helper()
	batch, err := contentblock.BatchFromRichTextSystemProto(
		state.Snapshot.Document.ID,
		&contentv1.RichTextBlockMutationBatch{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
			ExpectedRevision:        state.Snapshot.Document.Revision.String(),
			LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
				Locale: state.Locale,
				Mutations: []*contentv1.RichTextBlockLocaleMutation{{
					Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{
						Upsert: &contentv1.UpsertRichTextBlockLocale{Block: &contentv1.RichTextBlockLocale{
							BlockId: blockID.String(),
							Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
								Props: &contentv1.ParagraphLocaleProps{},
								Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
									Text: &contentv1.RichTextStyledText{Text: text},
								}}},
							}},
						}},
					},
				}},
			}},
		},
	)
	require.NoError(t, err)
	batch.ContributorMemberIDs = []uuid.UUID{uuid.MustParse(state.ViewerMemberID)}
	return batch
}

func workTargetCASIntegrationDocument(blockID uuid.UUID, locale, text string) *contentv1.RichTextDocument {
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK, SourceLocale: locale,
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID.String(), Value: &contentv1.RichTextBlock_Paragraph{
				Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
			}},
			Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{Locale: locale, Blocks: []*contentv1.RichTextBlockLocale{{
			BlockId: blockID.String(), Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
				Props:   &contentv1.ParagraphLocaleProps{},
				Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: text}}}},
			}},
		}}}},
	}
}

func workTargetCASIntegrationOverlay(blockID uuid.UUID, locale, text string) *contentv1.RichTextLocaleOverlay {
	return &contentv1.RichTextLocaleOverlay{Locale: locale, Blocks: []*contentv1.RichTextBlockLocale{{
		BlockId: blockID.String(), Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
			Props:   &contentv1.ParagraphLocaleProps{},
			Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: text}}}},
		}},
	}}}
}

func TestWorkAIDocumentExactMutationAuthorizesLockedLifecycleOnceIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	identityID := testutil.IntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, identityID, "Work AI document exact mutation")
	ctx := workIntegrationAdminCtx(identityID)
	workService := newWorkIntegrationService(t, db, identityID, referenceNoopFileDeleter{})
	isPresent := true
	created, err := workService.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title:     "Work AI document exact mutation " + testutil.IntegrationUUID(),
		Type:      managev1.WorkType_WORK_TYPE_ARTICLE,
		Year:      2026,
		Month:     8,
		IsPresent: &isPresent,
		Document:  emptyWorkIntegrationDocument("en"),
	}))
	require.NoError(t, err)
	store, err := contentblock.NewGeneratedStore(newContentBlockFileReuseAuthorizer(integrationSpiceDB(t)))
	require.NoError(t, err)

	assertCompilerBoundary := func(
		allowed bool,
		wantAction func(string) (policyv1.Can, error),
	) {
		t.Helper()
		checker := newCountingWorkPermissionServer(t, allowed)
		application := newWorkServiceForTest(t, workdomain.Deps{DB: db, SpiceDB: checker.client, ContentBlocks: store, Checkpoints: testcollaboration.NewCheckpoints(db, integrationSpiceDB(t))})
		compilerFailure := errors.New("stop after authorized Work compiler")
		compilerCalls := 0
		_, err = application.ExecuteAIDocumentMutation(
			ctx,
			created.Msg.Id,
			"en",
			workdomain.AIDocumentExecutionValidate,
			func(state workdomain.AIDocumentState) (workdomain.AIDocumentMutation, error) {
				compilerCalls++
				require.Equal(t, created.Msg.Id, state.Work.ID)
				return workdomain.AIDocumentMutation{}, compilerFailure
			},
		)
		if allowed {
			require.ErrorIs(t, err, compilerFailure)
			require.Equal(t, 1, compilerCalls)
		} else {
			require.Equal(t, connect.CodeNotFound, errs.CodeOf(err))
			require.Zero(t, compilerCalls, "authorization denial reached the Work compiler")
		}
		want, err := wantAction(created.Msg.Id)
		require.NoError(t, err)
		require.Equal(t, []string{want.Action().Permission()}, checker.permissions)
		require.Equal(t, []string{created.Msg.Id}, checker.resourceIDs)
	}

	assertCompilerBoundary(true, policyv1.Work.Edit)
	require.NoError(t, db.Table("work").Where("id = ?", created.Msg.Id).
		Update("status", managev1.WorkStatus_WORK_STATUS_ARCHIVED.String()).Error)
	assertCompilerBoundary(true, policyv1.Work.EditArchived)
	assertCompilerBoundary(false, policyv1.Work.EditArchived)

	checker := newCountingWorkPermissionServer(t, true)
	application := newWorkServiceForTest(t, workdomain.Deps{DB: db, SpiceDB: checker.client, ContentBlocks: store, Checkpoints: testcollaboration.NewCheckpoints(db, integrationSpiceDB(t))})
	var beforeTitle string
	var beforeRevision uuid.UUID
	require.NoError(t, db.Raw(`
		SELECT translation.title, document.revision
		FROM work
		JOIN work_translation AS translation
		  ON translation.entity_id = work.id
		 AND translation.locale = 'en'
		JOIN content_document AS document ON document.id = work.content_document_id
		WHERE work.id = ?
	`, created.Msg.Id).Row().Scan(&beforeTitle, &beforeRevision))
	replacement := "Work Validate must roll back"
	result, err := application.ExecuteAIDocumentMutation(
		ctx,
		created.Msg.Id,
		"en",
		workdomain.AIDocumentExecutionValidate,
		func(state workdomain.AIDocumentState) (workdomain.AIDocumentMutation, error) {
			contributor := uuid.MustParse(state.ViewerMemberID)
			return workdomain.AIDocumentMutation{
				WorkID:               state.Work.ID,
				Locale:               state.Locale,
				ObservedSourceLocale: state.SourceLocale,
				ExpectedRevision:     state.Snapshot.Document.Revision,
				ObservedLocaleExists: state.LocaleExists,
				ContributorMemberID:  state.ViewerMemberID,
				Metadata:             workdomain.AIDocumentMetadataPatch{SetTitle: true, Title: &replacement},
				Batch: contentblock.Batch{
					DocumentID:           state.Snapshot.Document.ID,
					ExpectedRevision:     state.Snapshot.Document.Revision,
					ContributorMemberIDs: []uuid.UUID{contributor},
				},
			}, nil
		},
	)
	require.NoError(t, err)
	require.True(t, result.Content.Changed)
	var afterTitle string
	var afterRevision uuid.UUID
	require.NoError(t, db.Raw(`
		SELECT translation.title, document.revision
		FROM work
		JOIN work_translation AS translation
		  ON translation.entity_id = work.id
		 AND translation.locale = 'en'
		JOIN content_document AS document ON document.id = work.content_document_id
		WHERE work.id = ?
	`, created.Msg.Id).Row().Scan(&afterTitle, &afterRevision))
	require.Equal(t, beforeTitle, afterTitle)
	require.Equal(t, beforeRevision, afterRevision)
	wantArchived, err := policyv1.Work.EditArchived(created.Msg.Id)
	require.NoError(t, err)
	require.Equal(t, []string{wantArchived.Action().Permission()}, checker.permissions)
}

// countingWorkPermissionServer answers every SpiceDB check with allowed and
// records the permission and resource of each.
type countingWorkPermissionServer struct {
	authzedv1.UnimplementedPermissionsServiceServer
	client      *auth.SpiceDBClient
	allowed     bool
	mu          sync.Mutex
	permissions []string
	resourceIDs []string
}

func newCountingWorkPermissionServer(t *testing.T, allowed bool) *countingWorkPermissionServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	counting := &countingWorkPermissionServer{allowed: allowed}
	authzedv1.RegisterPermissionsServiceServer(server, counting)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	counting.client, err = auth.NewSpiceDBClient(listener.Addr().String(), "test-token", true)
	require.NoError(t, err)
	t.Cleanup(func() { _ = counting.client.Close() })
	return counting
}

func (c *countingWorkPermissionServer) CheckPermission(_ context.Context, request *authzedv1.CheckPermissionRequest) (*authzedv1.CheckPermissionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.permissions = append(c.permissions, request.GetPermission())
	c.resourceIDs = append(c.resourceIDs, request.GetResource().GetObjectId())
	permissionship := authzedv1.CheckPermissionResponse_PERMISSIONSHIP_NO_PERMISSION
	if c.allowed {
		permissionship = authzedv1.CheckPermissionResponse_PERMISSIONSHIP_HAS_PERMISSION
	}
	return &authzedv1.CheckPermissionResponse{Permissionship: permissionship}, nil
}

// applyWorkAIDocument applies an already compiled mutation.
func applyWorkAIDocument(ctx context.Context, application *workdomain.Service, mutation workdomain.AIDocumentMutation) (workdomain.AIDocumentMutationResult, error) {
	return application.ExecuteAIDocumentMutation(
		ctx, mutation.WorkID, mutation.Locale, workdomain.AIDocumentExecutionApply,
		func(workdomain.AIDocumentState) (workdomain.AIDocumentMutation, error) { return mutation, nil },
	)
}
