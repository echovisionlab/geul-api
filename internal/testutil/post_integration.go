//go:build integration

package testutil

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func NewPostIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := PrepareOryIntegrationTest(t)
	require.NotNil(t, stack)
	return stack.DB
}

func NewConcurrentPostIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := PrepareOryIntegrationConcurrentTest(t)
	require.NotNil(t, stack)
	db, err := gorm.Open(gormpostgres.Open(stack.PostgresDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db
}

func PostIntegrationUUID() string { return uuid.NewString() }

func PostIntegrationMemberID(identityID string) string {
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("geul-integration-member:"+identityID))
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}

func SeedPostIntegrationIdentity(t *testing.T, db *gorm.DB, identityID, name string) string {
	t.Helper()
	email := identityID + "@example.test"
	SeedKratosIdentityFixture(t, db, KratosIdentityFixture{ID: identityID, Email: email, Name: name})
	memberID := PostIntegrationMemberID(identityID)
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
			ID: memberID, AccountIdentityID: &identityID, Nickname: name, Onboarded: true,
			PrimaryEmail: &email, AvailableEmails: []string{email}, SocialLinks: map[string]string{},
			CreatedAt: now, UpdatedAt: now,
		}).Error
	}))
	return memberID
}

func PostIntegrationSpiceDB(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	return SetupOryStack(t).SpiceDBClient
}

func GrantPostIntegrationRole(t *testing.T, spiceDB *auth.SpiceDBClient, identityID string, role policyv1.RoleID) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, role)
	require.NoError(t, err)
}

func PostIntegrationContext(identityID string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(PostIntegrationMemberID(identityID)),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
}

func PostIntegrationIdentity(identityID, preferredLocale string) *auth.Identity {
	traits := map[string]interface{}{"name": "Post Integration Member"}
	if preferredLocale != "" {
		traits["preferred_locale"] = preferredLocale
	}
	return &auth.Identity{
		ID: identityID, ExternalID: PostIntegrationMemberID(identityID),
		State: auth.KratosStateActive, Traits: traits,
	}
}

type PostIdentityManager struct {
	identities map[string]*auth.Identity
}

func NewPostIdentityManager(identities ...*auth.Identity) *PostIdentityManager {
	manager := &PostIdentityManager{identities: make(map[string]*auth.Identity, len(identities))}
	for _, identity := range identities {
		if identity != nil {
			manager.identities[identity.ID] = identity
		}
	}
	return manager
}

func (m *PostIdentityManager) GetIdentity(_ context.Context, identityID string) (*auth.Identity, error) {
	identity := m.identities[identityID]
	if identity == nil {
		return nil, fmt.Errorf("identity %s was not configured", identityID)
	}
	return identity, nil
}

func (m *PostIdentityManager) GetIdentityWithIncludeCredential(ctx context.Context, identityID, _ string) (*auth.Identity, error) {
	return m.GetIdentity(ctx, identityID)
}

func (*PostIdentityManager) ListIdentities(context.Context, int, int) ([]*auth.Identity, int64, error) {
	return nil, 0, nil
}
func (*PostIdentityManager) UpdateIdentityTraits(context.Context, string, structured.Fields) error {
	return nil
}
func (*PostIdentityManager) UpdateIdentityVerifiableAddresses(context.Context, string, []auth.VerifiableAddress) error {
	return nil
}
func (*PostIdentityManager) UpdateIdentityMetadataAdmin(context.Context, string, structured.Fields) error {
	return nil
}
func (*PostIdentityManager) SetIdentityState(context.Context, string, string) error { return nil }
func (*PostIdentityManager) DeleteIdentitySessions(context.Context, string) error   { return nil }
func (*PostIdentityManager) DeleteIdentity(context.Context, string) error           { return nil }
func (m *PostIdentityManager) GetIdentityEmail(ctx context.Context, identityID string) (string, error) {
	identity, err := m.GetIdentity(ctx, identityID)
	if err != nil {
		return "", err
	}
	return identity.CurrentEmail(), nil
}

type postIntegrationFileReuseAuthorizer struct{}

func (postIntegrationFileReuseAuthorizer) AuthorizeFileReuse(
	context.Context, *gorm.DB, contentblock.Document, contentblock.FullBlock,
	contentblock.FileReference, contentblock.File,
) error {
	return nil
}

func NewPostContentBlockStore(t *testing.T) *contentblock.Store {
	t.Helper()
	store, err := contentblock.NewGeneratedStore(postIntegrationFileReuseAuthorizer{})
	require.NoError(t, err)
	return store
}

func EmptyPostDocument(locale string) *contentv1.RichTextDocument {
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		SourceLocale:            locale,
		Base:                    &contentv1.RichTextBlockGraph{},
		LocaleOverlays:          []*contentv1.RichTextLocaleOverlay{{Locale: locale}},
	}
}

func SeedPostContentDocument(t *testing.T, db *gorm.DB) string {
	t.Helper()
	documentID := PostIntegrationUUID()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?::uuid, 'post')`, documentID).Error)
	return documentID
}

func SeedPostBaseRow(t *testing.T, db *gorm.DB, status string) string {
	t.Helper()
	postID := PostIntegrationUUID()
	documentID := SeedPostContentDocument(t, db)
	require.NoError(t, db.Exec(`
		INSERT INTO post (id, content_document_id, status, comments_enabled, created_at, updated_at)
		VALUES (?::uuid, ?::uuid, ?, TRUE, NOW(), NOW())
	`, postID, documentID, status).Error)
	return postID
}

func SeedPostIntegrationCategory(t *testing.T, db *gorm.DB, name, slug string) string {
	t.Helper()
	id := PostIntegrationUUID()
	require.NoError(t, db.Exec(
		`INSERT INTO category (id, name, slug, created_at) VALUES (?::uuid, ?, ?, NOW())`,
		id, name, slug,
	).Error)
	return id
}

func SeedPostIntegrationTag(t *testing.T, db *gorm.DB, name, slug string) string {
	t.Helper()
	id := PostIntegrationUUID()
	require.NoError(t, db.Exec(
		`INSERT INTO tag (id, name, slug, created_at) VALUES (?::uuid, ?, ?, NOW())`,
		id, name, slug,
	).Error)
	return id
}

func RequirePostMemberRelation(t *testing.T, db *gorm.DB, table, postID, memberID string, want bool) {
	t.Helper()
	var count int64
	require.NoError(t, db.Table(table).
		Where("post_id = ?::uuid AND member_id = ?::uuid", postID, memberID).
		Count(&count).Error)
	if want {
		require.EqualValues(t, 1, count)
		return
	}
	require.Zero(t, count)
}

func InsertPostIntegrationSession(t *testing.T, db *gorm.DB, identityID string) string {
	t.Helper()
	sessionID := PostIntegrationUUID()
	require.NoError(t, db.Exec(`
		INSERT INTO kratos.sessions (
			id, identity_id, active, authenticated_at, expires_at,
			created_at, updated_at, nid, authentication_methods
		)
		SELECT ?::uuid, id, TRUE, NOW(), NOW() + INTERVAL '1 hour',
		       NOW(), NOW(), nid, '[]'::jsonb
		FROM kratos.identities WHERE id = ?::uuid
	`, sessionID, identityID).Error)
	return sessionID
}

func RequirePostAuthorization(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	postID, identityID string,
	canFor func(string) (policyv1.Can, error),
	want bool,
) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	can, err := canFor(postID)
	require.NoError(t, err)
	allowed, err := spiceDB.CheckActorCan(t.Context(), actor, can)
	require.NoError(t, err)
	require.Equal(t, want, allowed)
}

func CountPostIntegrationRows(t *testing.T, db *gorm.DB, table, query string, args ...interface{}) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Table(table).Where(query, args...).Count(&count).Error)
	return count
}

type QueryCounter struct {
	logger.Interface
	Count *atomic.Int64
}

func (counter QueryCounter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	counter.Count.Add(1)
	counter.Interface.Trace(ctx, begin, fc, err)
}
