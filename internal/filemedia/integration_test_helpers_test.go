//go:build integration

package filemedia

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	memberdomain "github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/model"
	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	releasepostgres "github.com/echovisionlab/geul-api/internal/release/postgres"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type postSeriesAuditRow struct {
	Action        string
	TargetType    string `gorm:"column:target_type"`
	TargetID      string `gorm:"column:target_id"`
	ActorMemberID string `gorm:"column:actor_member_id"`
	RequestID     string `gorm:"column:request_id"`
	Attributes    []byte `gorm:"column:attributes"`
}

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start File/Media integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close File/Media integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func newConcurrentServiceIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := testutil.PrepareOryIntegrationConcurrentTest(t)
	require.NotNil(t, stack)
	return openConcurrentServiceIntegrationDB(t, stack)
}

// newLocalKratosConcurrentServiceIntegrationDB is for identity fences that
// lock kratos rows: the shared Kratos tables are postgres_fdw foreign tables
// whose remote REPEATABLE READ transactions do not wait like production.
func newLocalKratosConcurrentServiceIntegrationDB(t *testing.T) (*gorm.DB, *auth.SpiceDBClient) {
	t.Helper()
	stack := testutil.SetupOryStackWithLocalKratos(t)
	return openConcurrentServiceIntegrationDB(t, stack), stack.SpiceDBClient
}

func openConcurrentServiceIntegrationDB(t *testing.T, stack *testutil.OryStack) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(gormpostgres.Open(stack.PostgresDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db
}

func integrationSpiceDB(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	return testutil.SetupOryStack(t).SpiceDBClient
}

type integrationMemberSummaries struct {
	db        *gorm.DB
	cdnDomain string
}

func newIntegrationMemberSummaries(db *gorm.DB, cdnDomain string) MemberSummaries {
	return &integrationMemberSummaries{db: db, cdnDomain: cdnDomain}
}

func (s *integrationMemberSummaries) Load(
	ctx context.Context,
	memberIDs []string,
) (map[string]*commonv1.MemberSummary, error) {
	return memberdomain.LoadSummaries(ctx, s.db, s.cdnDomain, memberIDs)
}

func releaseAuditContext(t *testing.T, identityID, memberID string) context.Context {
	t.Helper()
	sessionID := testutil.IntegrationUUID()
	request, err := sharedtelemetry.NewPropagatedRequestContext(
		testutil.IntegrationUUID(),
		sharedtelemetry.MemberActor{
			IdentityID: identityID,
			MemberID:   memberID,
			SessionID:  sessionID,
		},
	)
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), request), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(sessionID), Authenticated: true, Onboarded: true,
	})
}

func integrationAdminCtxWithIdentityAndSpiceDB(t *testing.T, db *gorm.DB) (context.Context, *auth.SpiceDBClient) {
	t.Helper()
	spiceDB := integrationSpiceDB(t)
	return integrationAdminCtxWithIdentity(t, db, spiceDB), spiceDB
}

func integrationAdminCtxWithIdentity(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient) context.Context {
	t.Helper()
	identityID := testutil.IntegrationUUID()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "File/Media integration Admin")
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	return auditedUserContext(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(testutil.IntegrationUUID()), Authenticated: true, Onboarded: true,
	})
}

func seedFileDeliveryPostAuthority(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	postID string,
	identityID string,
) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	grant, err := policyv1.Post.TouchAuthor(postID, actor)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), grant)
	require.NoError(t, err)
}

func seedAudiencePostFileAttachment(t *testing.T, db *gorm.DB, postID, fileID string) {
	t.Helper()
	documentID := uuid.NewString()
	blockID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'post', ?::uuid)`,
		documentID, uuid.NewString(),
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO post (id, content_document_id) VALUES (?::uuid, ?::uuid)`,
		postID, documentID,
	).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block (
			id, document_id, parent_block_id, container_slot, position, kind, shared_data
		) VALUES (?::uuid, ?::uuid, NULL, 'root', 0, 'file', '{}'::jsonb)
	`, blockID, documentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id)
		VALUES (?::uuid, 'file', 'active', ?::uuid)
	`, blockID, fileID).Error)
}

func newEditorUploadSerializationFileService(stack *testutil.RuntimeStack, s3Client *s3.Client) *Service {
	return newTestService(Deps{
		DB:             stack.DB,
		S3:             s3Client,
		AsyncPublisher: &hardCutAsyncPublisher{},
		S3Bucket:       stack.S3MediaBucket,
		CDNDomain:      stack.CDNURL,
		MediaDomain:    stack.MediaURL,
		MediaSecret:    stack.MediaSigningSecret,
		Transcoder:     &recordingFileTranscoderPublisher{},
		SpiceDB:        stack.SpiceDBClient,
	})
}

type directTrackAttachment struct {
	audit domainaudit.Appender
}

func newDirectTrackAttachment(db *gorm.DB) TrackAttachment {
	return &directTrackAttachment{audit: apitelemetry.NewDurableWriter(db)}
}

func (a *directTrackAttachment) LockExistsWithDB(ctx context.Context, tx *gorm.DB, trackID string) error {
	return releaseConnectError(releasepkg.LockTrackForUpload(ctx, releasepostgres.NewStore(tx, releasepostgres.Ports{}), trackID))
}

func (a *directTrackAttachment) AttachOriginalWithDB(
	ctx context.Context,
	tx *gorm.DB,
	input TrackOriginalAudioInput,
) (TrackOriginalAudioAttachment, error) {
	attachment, err := releasepkg.AttachTrackOriginalAudio(ctx, releasepostgres.Bind(tx, releasepostgres.Ports{}, a.audit), releasepkg.TrackOriginalAudioInput{
		TrackID:               input.TrackID,
		VerifiedFileID:        input.VerifiedFileID,
		ExpectedCurrentFileID: input.ExpectedCurrentFileID,
	})
	return TrackOriginalAudioAttachment{
		AlreadyApplied: attachment.AlreadyApplied,
		CurrentFileID:  attachment.CurrentFileID,
		ReleaseID:      attachment.ReleaseID,
	}, releaseConnectError(err)
}

// releaseConnectError gives a Release answer its Connect form, as the
// production Track attachment adapter does.
func releaseConnectError(err error) error {
	if classified, ok := errs.Classified(err); ok {
		return classified
	}
	return err
}

type flakyAttachedConfirmPublisher struct {
	hardCutAsyncPublisher
	failuresRemaining int
}

func (p *flakyAttachedConfirmPublisher) NotifyProtobuf(ctx context.Context, signal string, msg proto.Message) error {
	if err := p.hardCutAsyncPublisher.NotifyProtobuf(ctx, signal, msg); err != nil {
		return err
	}
	if signal == eventpkg.SignalFileIngest &&
		msg.ProtoReflect().Descriptor().FullName() == (&managev1.FileIngestAttachedEvent{}).ProtoReflect().Descriptor().FullName() &&
		p.failuresRemaining > 0 {
		p.failuresRemaining--
		return errors.New("injected file ingest attached confirm failure")
	}
	return nil
}

func loadEditorUploadSession(t *testing.T, db *gorm.DB, uploadID string) model.UploadSession {
	t.Helper()
	var session model.UploadSession
	require.NoError(t, db.First(&session, "upload_id = ?", uploadID).Error)
	return session
}

func requireEditorUploadSessionStatus(t *testing.T, db *gorm.DB, uploadID string, status model.UploadSessionStatus) {
	t.Helper()
	require.Equal(t, status, loadEditorUploadSession(t, db, uploadID).Status)
}

func requireEditorUploadSessionAbsent(t *testing.T, db *gorm.DB, uploadID string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&model.UploadSession{}).Where("upload_id = ?", uploadID).Count(&count).Error)
	require.Zero(t, count)
}

// memberRequestContext carries the request context the domain audit
// attributes its records to, without a principal, for a File boundary that
// audits but neither authorizes nor attributes the upload to its caller.
func memberRequestContext() context.Context {
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{
		IdentityID: uuid.NewString(), MemberID: uuid.NewString(), SessionID: uuid.NewString(),
	})
	if err != nil {
		panic(err)
	}
	return sharedtelemetry.WithRequestContext(context.Background(), request)
}

func seedFileDeliveryContentPolicy(t *testing.T, spiceDB *auth.SpiceDBClient, resourceType string, resourceID string) {
	t.Helper()
	var policy policyv1.RelationshipMutation
	var err error
	switch resourceType {
	case "post":
		policy, err = policyv1.Post.TouchPolicy(resourceID)
	case "page":
		policy, err = policyv1.Page.TouchPolicy(resourceID)
	case "work":
		policy, err = policyv1.Work.TouchPolicy(resourceID)
	case "program_event":
		policy, err = policyv1.ProgramEvent.TouchPolicy(resourceID)
	default:
		require.FailNow(t, "unsupported FileDelivery content policy resource", resourceType)
		return
	}
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)
}

func decodeHardCutRoutedMessages[T proto.Message](
	t *testing.T,
	messages []hardCutPublishedMessage,
	exchange string,
	routingKey string,
	newMessage func() T,
) []T {
	t.Helper()
	decoded := make([]T, 0)
	for _, message := range messages {
		target := newMessage()
		if message.exchange != exchange || message.key != routingKey ||
			message.messageType != string(target.ProtoReflect().Descriptor().FullName()) {
			continue
		}
		require.NoError(t, proto.Unmarshal(message.body, target))
		decoded = append(decoded, target)
	}
	return decoded
}

func hardCutPtrString(value string) *string { return &value }

func seedHardCutPageFixture(t *testing.T, db *gorm.DB) string {
	t.Helper()
	pageID := uuid.NewString()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile) VALUES (?, 'page')`,
		documentID,
	).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO page (id, content_document_id, status, show_title, created_at, updated_at)
		VALUES (?, ?, 'PAGE_STATUS_DRAFT', TRUE, NOW(), NOW())
	`, pageID, documentID).Error)
	return pageID
}
