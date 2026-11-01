//go:build integration

package programevent_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/programevent"
	programeventpostgres "github.com/echovisionlab/geul-api/internal/programevent/postgres"
	"github.com/echovisionlab/geul-api/internal/translation"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"

	artistadapter "github.com/echovisionlab/geul-api/internal/adapters/artist"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	programeventadapter "github.com/echovisionlab/geul-api/internal/adapters/programevent"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func programEventIntegrationCheckpoints(db *gorm.DB, spiceDB *auth.SpiceDBClient) persistencecheckpoint.ContributorFence {
	return testcollaboration.NewCheckpoints(db, spiceDB)
}

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Program Event integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close Program Event integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func newProgramEventCreditMemberSummaries(db *gorm.DB, cdnDomain string) programevent.CreditMemberSummaries {
	return programeventadapter.NewCreditMemberSummaries(db, cdnDomain)
}

func newProgramEventRuntime(cdnDomain string) programeventpostgres.Media {
	return programeventadapter.NewRuntime(cdnDomain)
}

// integrationDeps are the collaborators an integration test overrides;
// unset ones get the integration defaults.
type integrationDeps struct {
	Audit         domainaudit.Appender
	ContentBlocks *contentblock.Store
	Signals       contentsignal.Publisher
	Media         programeventpostgres.Media
	MediaHydrator programeventpostgres.Files
	CreditMembers programevent.CreditMemberSummaries
	Checkpoints   persistencecheckpoint.ContributorFence
}

// newIntegrationService builds the Program Event use cases on the
// PostgreSQL Store. Collaborators left unset in deps get the integration
// defaults: a recording audit appender, a content Block store, a signal
// publisher, the public-asset runtime, the File media hydrator, credit
// Member summaries and collaboration checkpoints.
func newIntegrationService(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient, deps integrationDeps) *programevent.Service {
	t.Helper()
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	if deps.ContentBlocks == nil {
		deps.ContentBlocks = newProgramEventIntegrationContentBlockStore(t, spiceDB)
	}
	if deps.Signals == nil {
		deps.Signals = &capturingAsyncPublisher{}
	}
	if deps.Media == nil {
		deps.Media = newProgramEventRuntime("https://cdn.example.com")
	}
	if deps.MediaHydrator == nil {
		deps.MediaHydrator = newProgramEventIntegrationFileService(db, spiceDB)
	}
	if deps.CreditMembers == nil {
		deps.CreditMembers = newProgramEventCreditMemberSummaries(db, "https://cdn.example.com")
	}
	if deps.Checkpoints == nil {
		deps.Checkpoints = programEventIntegrationCheckpoints(db, spiceDB)
	}
	units, err := programeventpostgres.NewUnits(db, programeventpostgres.Ports{
		ContentBlocks: deps.ContentBlocks, Signals: deps.Signals, Media: deps.Media, Files: deps.MediaHydrator,
		Members: memberadapter.ContentMembers{}, CreditArtists: artistadapter.CreditArtists{}, Checkpoints: deps.Checkpoints,
	})
	require.NoError(t, err)
	service, err := programevent.NewService(programevent.Deps{
		Units: units, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: deps.Audit, CreditMembers: deps.CreditMembers,
	})
	require.NoError(t, err)
	return service
}

func typeHandlerForTest(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient) *programevent.TypeConnectHandler {
	t.Helper()
	return programevent.NewTypeConnectHandler(newIntegrationService(t, db, spiceDB, integrationDeps{}))
}

// requestBackground carries the request context a request-attributed audit
// record needs; the caller's user becomes its actor.
func requestBackground() context.Context {
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.98")
	if err != nil {
		panic(err)
	}
	return sharedtelemetry.WithRequestContext(context.Background(), requestContext)
}

func integrationSpiceDB(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	return testutil.SetupOryStack(t).SpiceDBClient
}

func integrationAdminCtxWithIdentityAndSpiceDB(t *testing.T, db *gorm.DB) (context.Context, *auth.SpiceDBClient) {
	t.Helper()
	identityID := testutil.IntegrationUUID()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Program Event integration Admin")
	spiceDB := integrationSpiceDB(t)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	return auth.WithUser(requestBackground(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	}), spiceDB
}

func programEventAuditedMemberContext(t *testing.T, identityID, memberID string) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.77")
	require.NoError(t, err)
	ctx := sharedtelemetry.WithRequestContext(t.Context(), requestContext)
	return auth.WithUser(ctx, &auth.UserInfo{
		SessionID: auth.SessionID(uuid.NewString()), IdentityID: auth.IdentityID(identityID),
		MemberID: auth.MemberID(memberID), Authenticated: true, Onboarded: true,
	})
}

func programEventAuditedSystemContext(t *testing.T) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(
		uuid.NewString(), sharedtelemetry.SystemActor{ServiceName: sharedtelemetry.ServiceEditorCollab},
	)
	require.NoError(t, err)
	return sharedtelemetry.WithRequestContext(t.Context(), requestContext)
}

func ptrString(value string) *string { return &value }
func stringPtr(value string) *string { return &value }

func lockAdminMutationRoot(t *testing.T, db *gorm.DB, table, condition string) *gorm.DB {
	t.Helper()
	tx := db.Begin()
	require.NoError(t, tx.Error)
	var rootID string
	require.NoError(t, tx.Raw("SELECT id::text FROM "+table+" WHERE "+condition+" FOR UPDATE").Scan(&rootID).Error)
	require.NotEmpty(t, rootID)
	t.Cleanup(func() { _ = tx.Rollback().Error })
	return tx
}

func demoteAdminMutationActor(t *testing.T, spiceDB *auth.SpiceDBClient, ctx context.Context) {
	t.Helper()
	principal := auth.GetUser(ctx)
	require.NotNil(t, principal)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, principal.IdentityID.String(), policyv1.Role.User())
}

func requireAdminMutationWaiting(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		require.FailNow(t, "mutation returned before its authoritative root lock was released", "error: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
}

func seedImageBindingUploadedFileFixture(t *testing.T, db *gorm.DB, key string) string {
	return seedImageBindingUploadedFileFixtureForKind(t, db, key, "image")
}

func seedImageBindingUploadedFileFixtureForKind(t *testing.T, db *gorm.DB, key, kind string) string {
	t.Helper()
	id := uuid.NewString()
	digest := sha256.Sum256([]byte(key))
	require.NoError(t, db.Exec(
		`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256)
		 VALUES (?, ?, 'image/webp', 1024, 'webp', ?)`,
		id,
		id,
		digest[:],
	).Error)
	lifecycle := mediaasset.NewLifecycle(db, "https://cdn.example.com")
	asset, _, err := lifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		SourceFileID: &id,
		Kind:         kind,
		Extension:    "webp",
		MimeType:     "image/webp",
		Disposition:  commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	assetDigest := sha256.Sum256([]byte(asset.ID))
	_, err = lifecycle.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{
		AssetId: asset.ID, FileSize: 1024, Sha256: assetDigest[:],
	})
	require.NoError(t, err)
	return id
}

const creativeContentProfile = "compact"

func seedServiceIntegrationContentDocument(t *testing.T, db *gorm.DB, profile string) string {
	t.Helper()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile) VALUES (?::uuid, ?)`, documentID, profile,
	).Error)
	return documentID
}

type failingDomainAuditAppender struct{}

func (failingDomainAuditAppender) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return errors.New("audit unavailable")
}

type capturingAsyncPublisher struct{}

func (*capturingAsyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

func (*capturingAsyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}

func (publisher *capturingAsyncPublisher) NotifyProtobufWithExecutor(
	ctx context.Context,
	_ eventpkg.DBTX,
	signal string,
	message proto.Message,
) error {
	return publisher.NotifyProtobuf(ctx, signal, message)
}

// loadEventMessage reads the administrator view of an event the caller may
// view.
func loadEventMessage(ctx context.Context, handler *programevent.ConnectHandler, eventID string) (*managev1.ProgramEvent, error) {
	response, err := handler.GetProgramEvent(ctx, connect.NewRequest(&managev1.GetProgramEventRequest{Id: eventID}))
	if err != nil {
		return nil, err
	}
	return response.Msg, nil
}

// loadTranslationSource reads the event's translation source in the
// read-committed transaction translation delivery opens.
func loadTranslationSource(ctx context.Context, db *gorm.DB, store *contentblock.Store, eventID string) (*translation.SourceDocument, error) {
	var output *translation.SourceDocument
	err := mutation.ReadCurrent(ctx, db, func(tx *gorm.DB) error {
		var err error
		output, err = programevent.LoadTranslationSourceDocument(ctx, programeventpostgres.Bind(tx, programeventpostgres.Ports{ContentBlocks: store}, nil).Store, eventID)
		return err
	})
	return output, err
}

func translationJob(job *model.TranslationJob) programevent.TranslationJob {
	return programevent.TranslationJob{
		EntityType: job.EntityType, EntityID: job.EntityID, TargetLocale: job.TargetLocale,
		RequestedByMemberID: job.RequestedByMemberID,
	}
}

func programEventParagraphOverlay(blockID string, locale string, text string) *contentv1.RichTextLocaleOverlay {
	return &contentv1.RichTextLocaleOverlay{Locale: locale, Blocks: []*contentv1.RichTextBlockLocale{{
		BlockId: blockID,
		Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
			Props: &contentv1.ParagraphLocaleProps{},
			Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
				Text: &contentv1.RichTextStyledText{Text: text},
			}}},
		}},
	}}}
}

type programEventAIDocumentTestPublisher struct {
	calls   int
	signal  string
	message proto.Message
	err     error
}

func (publisher *programEventAIDocumentTestPublisher) NotifyProtobuf(_ context.Context, signal string, message proto.Message) error {
	publisher.calls++
	publisher.signal = signal
	publisher.message = message
	return publisher.err
}

func (*programEventAIDocumentTestPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}

func (publisher *programEventAIDocumentTestPublisher) NotifyProtobufWithExecutor(ctx context.Context, _ eventpkg.DBTX, signal string, message proto.Message) error {
	return publisher.NotifyProtobuf(ctx, signal, message)
}
