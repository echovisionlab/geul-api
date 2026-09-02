//go:build integration

package programevent

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	programeventadapter "github.com/echovisionlab/geul-api/internal/adapters/programevent"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
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

func newServiceIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := testutil.PrepareOryIntegrationTest(t)
	require.NotNil(t, stack)
	return stack.DB
}

func newProgramEventCreditMemberSummaries(db *gorm.DB, cdnDomain string) CreditMemberSummaries {
	return programeventadapter.NewCreditMemberSummaries(db, cdnDomain)
}

func newProgramEventRuntime(cdnDomain string) Runtime {
	return programeventadapter.NewRuntime(cdnDomain)
}

func integrationTestUUID() string { return uuid.NewString() }

func integrationMemberID(identityID string) string {
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("geul-program-event-integration-member:"+identityID))
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}

func integrationSpiceDB(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	return testutil.SetupOryStack(t).SpiceDBClient
}

func grantIntegrationGlobalRole(t *testing.T, spiceDB *auth.SpiceDBClient, identityID string, role policyv1.RoleID) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, role)
	require.NoError(t, err)
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

func integrationAdminCtxWithIdentityAndSpiceDB(t *testing.T, db *gorm.DB) (context.Context, *auth.SpiceDBClient) {
	t.Helper()
	identityID := integrationTestUUID()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Program Event integration Admin")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	return auth.WithUser(context.Background(), &auth.UserInfo{
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
	grantIntegrationGlobalRole(t, spiceDB, principal.IdentityID.String(), policyv1.Role.User())
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

type referenceNoopFileDeleter struct{}

func (referenceNoopFileDeleter) DeleteFileByID(context.Context, string) error { return nil }

type capturingAsyncPublisher struct{}

func (*capturingAsyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

func (*capturingAsyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}
