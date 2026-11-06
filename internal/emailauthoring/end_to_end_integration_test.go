//go:build integration

package emailauthoring_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil/contenttest"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	emailauthoringpostgres "github.com/echovisionlab/geul-api/internal/emailauthoring/postgres"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	// endToEnd is the Email Authoring use cases as the server wires them, over
	// a database and SpiceDB.
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
)

type endToEnd struct {
	db       *gorm.DB
	ctx      context.Context
	spiceDB  *auth.SpiceDBClient
	ports    emailauthoringpostgres.Ports
	service  *emailauthoring.Service
	internal *emailauthoring.InternalService
}

func newEndToEnd(t *testing.T, db *gorm.DB) endToEnd {
	t.Helper()
	ctx, spiceDB := orytest.IntegrationAdminAuditContext(t, db)
	runtime := emailauthoringadapter.NewRuntime()
	ports := emailauthoringpostgres.Ports{
		ContentBlocks: contenttest.NewEmailContentBlockStore(t), References: emailauthoringadapter.NewCampaignDeliveryReferences(),
		Members: memberadapter.ContributorMembers{}, Checkpoints: testcollaboration.NewCheckpoints(db, spiceDB), Rendering: runtime,
	}
	units, err := emailauthoringpostgres.NewUnits(db, ports)
	require.NoError(t, err)
	audit := apitelemetry.NewDurableWriter(db)
	service, err := emailauthoring.NewService(emailauthoring.Deps{
		Units: units, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: audit,
		TestEmails: &testEmailQueue{}, Locales: runtime,
	})
	require.NoError(t, err)
	internal, err := emailauthoring.NewInternalService(emailauthoring.InternalDeps{
		Units: units, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: audit,
	})
	require.NoError(t, err)
	return endToEnd{db: db, ctx: ctx, spiceDB: spiceDB, ports: ports, service: service, internal: internal}
}

func uniqueKey(prefix string) string {
	return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// lockRow holds a FOR UPDATE lock on one row until the test releases it.
func lockRow(t *testing.T, db *gorm.DB, table, id string) *gorm.DB {
	t.Helper()
	tx := db.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { _ = tx.Rollback().Error })
	var locked string
	require.NoError(t, tx.Raw("SELECT id::text FROM "+table+" WHERE id = ? FOR UPDATE", id).Scan(&locked).Error)
	require.Equal(t, id, locked)
	return tx
}

// An administrator demoted while the template update waited on the root lock
// is denied once the lock is released: the fresh check runs after the lock.
func TestEmailTemplateUpdateRechecksAuthorityAfterWaitingOnItsRootIntegration(t *testing.T) {
	t.Parallel()
	e := newEndToEnd(t, orytest.NewConcurrentPostIntegrationDB(t))
	created, err := e.service.CreateEmailTemplate(e.ctx, emailauthoring.CreateEmailTemplateCommand{
		Key: uniqueKey("fence"), Name: "Original template", Subject: "Subject", SourceLocale: "en",
	})
	require.NoError(t, err)

	lockTx := lockRow(t, e.db, "email_template", created.ID)
	result := make(chan error, 1)
	go func() {
		name := "must not persist"
		_, err := e.service.UpdateEmailTemplate(e.ctx, emailauthoring.UpdateEmailTemplateCommand{ID: created.ID, Name: &name})
		result <- err
	}()
	pgtest.RequireLockWaiters(t, e.db, 1)
	orytest.GrantIntegrationGlobalRole(t, e.spiceDB, auth.GetUser(e.ctx).IdentityID.String(), policyv1.Role.User())
	require.NoError(t, lockTx.Commit().Error)

	require.Equal(t, connect.CodePermissionDenied, codeOf(<-result))
	var name string
	require.NoError(t, e.db.Table("email_template").Select("name").Where("id = ?", created.ID).Scan(&name).Error)
	require.Equal(t, "Original template", name)
}

// A layout deletion and a template assignment of the layout both lock the
// layout first, so whichever wins, neither deadlocks and the other answers
// the state the winner left.
func TestEmailLayoutDeleteAndTemplateAssignmentDoNotDeadlockIntegration(t *testing.T) {
	t.Parallel()
	e := newEndToEnd(t, orytest.NewConcurrentPostIntegrationDB(t))
	layout, err := e.service.CreateEmailLayout(e.ctx, emailauthoring.CreateEmailLayoutCommand{
		Name: "Race layout", Key: uniqueKey("race_layout"), HTMLContent: "<html><body>{{content}}</body></html>", SourceLocale: "en",
	})
	require.NoError(t, err)
	template, err := e.service.CreateEmailTemplate(e.ctx, emailauthoring.CreateEmailTemplateCommand{
		Key: uniqueKey("race_template"), Name: "Race template", Subject: "Race", SourceLocale: "en",
	})
	require.NoError(t, err)

	lockTx := lockRow(t, e.db, "email_layout", layout.ID)
	deleted, assigned := make(chan error, 1), make(chan error, 1)
	go func() {
		deleted <- e.service.DeleteEmailLayout(e.ctx, emailauthoring.DeleteEmailLayoutCommand{ID: layout.ID})
	}()
	go func() {
		_, err := e.service.UpdateEmailTemplate(e.ctx, emailauthoring.UpdateEmailTemplateCommand{ID: template.ID, LayoutID: &layout.ID})
		assigned <- err
	}()
	pgtest.RequireLockWaiters(t, e.db, 2)
	require.NoError(t, lockTx.Rollback().Error)
	deleteErr, assignErr := <-deleted, <-assigned

	var stored model.EmailTemplate
	require.NoError(t, e.db.First(&stored, "id = ?", template.ID).Error)
	if deleteErr == nil {
		require.Equal(t, connect.CodeNotFound, codeOf(assignErr))
		require.Nil(t, stored.LayoutID)
		return
	}
	require.NoError(t, assignErr)
	require.Equal(t, connect.CodeFailedPrecondition, codeOf(deleteErr))
	require.Equal(t, layout.ID, *stored.LayoutID)
}

// Collaboration, interchange and a provider delivery on the real content
// store share one document revision: a target write keeps it, a stale
// interchange loses to the newer target, the provider's sparse overlay
// falls back to the source, and a source edit refreshes the target
// projection and the custom variables.
func TestEmailTemplateTargetsKeepTheSharedRevisionAcrossWritersIntegration(t *testing.T) {
	t.Parallel()
	db := orytest.SetupOryStackWithLocalKratos(t).DB
	e := newEndToEnd(t, db)
	created, err := e.service.CreateEmailTemplate(e.ctx, emailauthoring.CreateEmailTemplateCommand{
		Key: uniqueKey("exact_target"), Name: "Exact target Template", Subject: "Source subject", SourceLocale: "en",
	})
	require.NoError(t, err)
	contributorID := orytest.IntegrationUUID()
	orytest.InsertAuthorizedDocumentContributor(t, db, e.spiceDB, contributorID)
	collab := orytest.NewAuditContext(t, auth.GetUser(e.ctx).IdentityID.String(), contributorID)

	sourceWrite, err := e.internal.ApplyTemplateBlockBatch(collab, emailauthoring.ApplyTemplateBlockBatchCommand{
		TemplateID: created.ID, Locale: "en",
		Batch: contenttest.NewParagraphBatch(created.Projection.Document, created.Projection.Revision, "en",
			"Source body {{ Recipient_Name }} {{custom_code}} {{recipient_name}}", []string{contributorID}),
	})
	require.NoError(t, err)
	var variables model.EmailTemplate
	require.NoError(t, db.Select("variables").First(&variables, "id = ?", created.ID).Error)
	require.Equal(t, []string{"custom_code", "recipient_name"}, []string{variables.Variables[0].Name, variables.Variables[1].Name})

	store := emailauthoringpostgres.NewStore(db, e.ports)
	createdTarget, err := e.internal.TemplateAIDocuments().ExecuteAIDocumentMutation(e.ctx, created.ID, "ko", emailcontent.AIDocumentExecutionApply,
		func(state emailcontent.AIDocumentState) (emailcontent.AIDocumentMutation, error) {
			return emailcontent.AIDocumentMutation{
				Reference: state.Reference, Locale: state.Locale, ExpectedDocumentRevision: state.DocumentRevision,
				ExpectedSource: state.SourceLocale, ExpectedPresence: state.LocaleExists,
				ContributorMember: uuid.MustParse(state.ViewerMemberID), CreateTranslation: true,
			}, nil
		})
	require.NoError(t, err)
	require.True(t, createdTarget.Changed)
	require.Equal(t, sourceWrite.DocumentRevision, createdTarget.DocumentRevision)
	source, err := emailauthoring.LoadTemplateTranslationSourceDocument(collab, store, created.ID)
	require.NoError(t, err)
	require.Equal(t, sourceWrite.DocumentRevision, source.ContentDocumentRevision)
	blockID := source.ContentBlockDocument.GetLocaleOverlay().GetBlocks()[0].GetBlockId()

	targetRevision := *createdTarget.TargetRevision
	targetWrite, err := e.internal.ApplyTemplateBlockBatch(collab, emailauthoring.ApplyTemplateBlockBatchCommand{
		TemplateID: created.ID, Locale: "ko", ExpectedTargetRevision: &targetRevision,
		Batch: paragraphBatch(uuid.MustParse(sourceWrite.DocumentRevision), "ko", blockID, "번역 본문", false, contributorID),
	})
	require.NoError(t, err)
	require.Equal(t, sourceWrite.DocumentRevision, targetWrite.DocumentRevision)

	stale := "must not win"
	_, err = emailauthoring.ApplyTemplateTranslationInterchange(collab, store, created.ID, "en", emailcontent.InterchangeMutation{
		TargetLocale: "ko", ExpectedDocumentRevision: sourceWrite.DocumentRevision, ExpectedTargetRevision: &targetRevision,
		ExpectedPresence: true, Subject: &stale, ContributorMemberID: contributorID, Now: time.Now().UTC(),
	})
	var targetConflict *translation.TargetRevisionConflict
	require.ErrorAs(t, err, &targetConflict)

	updated, err := e.internal.ApplyTemplateBlockBatch(collab, emailauthoring.ApplyTemplateBlockBatchCommand{
		TemplateID: created.ID, Locale: "en",
		Batch: paragraphBatch(uuid.MustParse(sourceWrite.DocumentRevision), "en", blockID, "Updated source body", false, contributorID),
	})
	require.NoError(t, err)
	require.NotEqual(t, sourceWrite.DocumentRevision, updated.DocumentRevision)
}

// A real domain write must join the shared transaction, and a second write of the
// same root inside it must not wait on the lock the first holds. The MCP text
// tools depend on both to change a document's locales together.
func TestEmailTemplateAIDocumentMutationJoinsASharedTransactionIntegration(t *testing.T) {
	t.Parallel()
	db := orytest.SetupOryStackWithLocalKratos(t).DB
	e := newEndToEnd(t, db)
	created, err := e.service.CreateEmailTemplate(e.ctx, emailauthoring.CreateEmailTemplateCommand{
		Key: uniqueKey("shared_tx"), Name: "Shared transaction Template", Subject: "Source subject", SourceLocale: "en",
	})
	require.NoError(t, err)
	documents := e.internal.TemplateAIDocuments()

	createTranslation := func(ctx context.Context) error {
		_, err := documents.ExecuteAIDocumentMutation(ctx, created.ID, "ko", emailcontent.AIDocumentExecutionApply,
			func(state emailcontent.AIDocumentState) (emailcontent.AIDocumentMutation, error) {
				return emailcontent.AIDocumentMutation{
					Reference: state.Reference, Locale: state.Locale, ExpectedDocumentRevision: state.DocumentRevision,
					ExpectedSource: state.SourceLocale, ExpectedPresence: state.LocaleExists,
					ContributorMember: uuid.MustParse(state.ViewerMemberID), CreateTranslation: true,
				}, nil
			})
		return err
	}
	translationExists := func(ctx context.Context) bool {
		state, err := documents.Load(ctx, created.ID, "ko")
		require.NoError(t, err)
		return state.LocaleExists
	}

	refused := errors.New("a later write of the same edit failed")
	err = mutation.InSharedTransaction(e.ctx, db, func(ctx context.Context) error {
		require.NoError(t, createTranslation(ctx))
		require.True(t, translationExists(ctx), "a read in the transaction must see its own write")
		return refused
	})
	require.ErrorIs(t, err, refused)
	require.False(t, translationExists(e.ctx), "the rollback must undo the finished domain mutation")

	require.NoError(t, mutation.InSharedTransaction(e.ctx, db, createTranslation))
	require.True(t, translationExists(e.ctx))
}
