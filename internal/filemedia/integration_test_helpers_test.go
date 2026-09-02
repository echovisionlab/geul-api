//go:build integration

package filemedia

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/auth"
	memberdomain "github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/model"
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

var fileIntegrationSuite *testutil.OryIntegrationSuite

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
	fileIntegrationSuite = suite
	code := m.Run()
	fileIntegrationSuite = nil
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close File/Media integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func newServiceIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := testutil.PrepareOryIntegrationTest(t)
	require.NotNil(t, stack)
	return stack.DB
}

func newConcurrentServiceIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := testutil.PrepareOryIntegrationConcurrentTest(t)
	require.NotNil(t, stack)
	require.NotNil(t, fileIntegrationSuite)
	db, err := gorm.Open(gormpostgres.Open(fileIntegrationSuite.Stack().PostgresDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db
}

func integrationTestUUID() string { return uuid.NewString() }

func integrationMemberID(identityID string) string {
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("geul-file-media-integration-member:"+identityID))
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
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
	sessionID := integrationTestUUID()
	request, err := sharedtelemetry.NewPropagatedRequestContext(
		integrationTestUUID(),
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

func grantIntegrationGlobalRole(t *testing.T, spiceDB *auth.SpiceDBClient, identityID string, role policyv1.RoleID) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, role)
	require.NoError(t, err)
}

func integrationAdminCtxWithIdentityAndSpiceDB(t *testing.T, db *gorm.DB) (context.Context, *auth.SpiceDBClient) {
	t.Helper()
	identityID := integrationTestUUID()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "File/Media integration Admin")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(integrationTestUUID()), Authenticated: true, Onboarded: true,
	}), spiceDB
}

func attachInternalResourcePolicy(t *testing.T, spiceDB *auth.SpiceDBClient, resourceID string) {
	t.Helper()
	mutation, err := policyv1.Page.TouchPolicy(resourceID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), mutation)
	require.NoError(t, err)
}

func durableAudienceSpiceDB(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	return testutil.SetupOryStack(t).SpiceDBClient
}

func seedExternalKratosIdentityWithTraits(t *testing.T, db *gorm.DB, identityID, name string) string {
	t.Helper()
	email := identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: email, Name: name,
	})
	memberID := integrationMemberID(identityID)
	now := time.Now().UTC()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", memberID, identityID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO account_identity (id, created_at)
			SELECT id, created_at FROM kratos.identities WHERE id = ?::uuid
			ON CONFLICT (id) DO NOTHING
		`, identityID).Error; err != nil {
			return err
		}
		return tx.Create(&model.Member{
			ID: memberID, AccountIdentityID: &identityID, Nickname: name,
			Onboarded: true, PrimaryEmail: &email, AvailableEmails: []string{email},
			SocialLinks: map[string]string{}, CreatedAt: now, UpdatedAt: now,
		}).Error
	}))
	return memberID
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

func seedAudienceSegmentRelationsForTest(
	t *testing.T,
	db *gorm.DB,
	segmentID string,
	config model.AudienceSegmentConfig,
) {
	t.Helper()
	for _, tagID := range config.MemberTagIDs {
		require.NoError(t, db.Create(&model.AudienceSegmentUserTag{AudienceSegmentID: segmentID, UserTagID: tagID}).Error)
	}
	for _, role := range config.AccountRoles {
		require.NoError(t, db.Create(&model.AudienceSegmentUserRole{AudienceSegmentID: segmentID, Role: role}).Error)
	}
	for _, memberID := range config.ExcludeMemberIDs {
		require.NoError(t, db.Create(&model.AudienceSegmentExcludedMember{AudienceSegmentID: segmentID, MemberID: memberID}).Error)
	}
}

type fileTestAudienceRecipientCounter struct{}

func (fileTestAudienceRecipientCounter) Count(context.Context, *model.AudienceSegment) (int64, error) {
	return 0, nil
}

type fileTestAudienceMemberReferences struct{}

func (fileTestAudienceMemberReferences) EligibleIDs(_ context.Context, _ *gorm.DB, memberIDs []string) ([]string, error) {
	return memberIDs, nil
}

func newAudienceServiceForTest(db *gorm.DB, spiceDB *auth.SpiceDBClient) *audience.AudienceService {
	return audience.NewAudienceService(db, spiceDB, fileTestAudienceRecipientCounter{}, fileTestAudienceMemberReferences{})
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

func newEditorUploadSerializationFileService(stack *testutil.RuntimeStack, s3Client *s3.Client) *FileService {
	return NewFileService(
		stack.DB, s3Client, &hardCutAsyncPublisher{}, stack.S3MediaBucket,
		stack.CDNURL, stack.MediaURL, stack.MediaSigningSecret,
		&recordingFileTranscoderPublisher{}, stack.SpiceDBClient,
	)
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
