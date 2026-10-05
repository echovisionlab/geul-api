//go:build integration

package label

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
	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "start Label integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	if err := testutil.RunIntegrationSuiteCleanups(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "cleanup Label integration suite: %v\n", err)
		code = 1
	}
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "close Label integration suite: %v\n", err)
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

func integrationTestUUID() string { return uuid.NewString() }

func integrationMemberID(identityID string) string {
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("dsub-label-integration-member:"+identityID))
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}

func seedExternalKratosIdentityWithTraits(t *testing.T, db *gorm.DB, identityID, name string) string {
	t.Helper()
	email := identityID + "@label.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Email: email, Name: name})
	memberID := integrationMemberID(identityID)
	now := time.Now().UTC()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid`, memberID, identityID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO account_identity (id, created_at) SELECT id, created_at FROM kratos.identities WHERE id = ?::uuid ON CONFLICT (id) DO NOTHING`, identityID).Error; err != nil {
			return err
		}
		return tx.Create(&model.Member{ID: memberID, AccountIdentityID: &identityID, Nickname: name, Onboarded: true, PrimaryEmail: &email, AvailableEmails: []string{email}, SocialLinks: map[string]string{}, CreatedAt: now, UpdatedAt: now}).Error
	}))
	return memberID
}

func artistIntegrationAdminCtx(identityID string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(integrationMemberID(identityID)),
		SessionID: auth.SessionID(integrationTestUUID()), Authenticated: true, Onboarded: true,
	})
}

func syncArtistIntegrationGlobalRole(t *testing.T, spiceDB *auth.SpiceDBClient, identityID string, role policyv1.RoleID) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, role)
	require.NoError(t, err)
}

type recordingArtistFileDeleter struct{ deletedIDs []string }

func (d *recordingArtistFileDeleter) DeleteFileByID(_ context.Context, fileID string) error {
	d.deletedIDs = append(d.deletedIDs, fileID)
	return nil
}

type noopArtistAsyncPublisher struct{}

func (noopArtistAsyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}
func (noopArtistAsyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

type capturingAsyncPublisher struct{ noopArtistAsyncPublisher }

type fakeIdentityManager struct{ identity *auth.Identity }

func (f *fakeIdentityManager) GetIdentity(_ context.Context, identityID string) (*auth.Identity, error) {
	if f.identity == nil || f.identity.ID != identityID {
		return nil, errors.New("Label integration identity not found")
	}
	return f.identity, nil
}
func (f *fakeIdentityManager) GetIdentityWithIncludeCredential(ctx context.Context, identityID, _ string) (*auth.Identity, error) {
	return f.GetIdentity(ctx, identityID)
}
func (*fakeIdentityManager) ListIdentities(context.Context, int, int) ([]*auth.Identity, int64, error) {
	return nil, 0, nil
}
func (*fakeIdentityManager) UpdateIdentityTraits(context.Context, string, structured.Fields) error {
	return nil
}
func (*fakeIdentityManager) UpdateIdentityMetadataAdmin(context.Context, string, structured.Fields) error {
	return nil
}
func (*fakeIdentityManager) UpdateIdentityVerifiableAddresses(context.Context, string, []auth.VerifiableAddress) error {
	return nil
}
func (*fakeIdentityManager) SetIdentityState(context.Context, string, string) error { return nil }
func (*fakeIdentityManager) DeleteIdentitySessions(context.Context, string) error   { return nil }
func (*fakeIdentityManager) DeleteIdentity(context.Context, string) error           { return nil }
func (f *fakeIdentityManager) GetIdentityEmail(context.Context, string) (string, error) {
	if f.identity == nil {
		return "", nil
	}
	return f.identity.CurrentEmail(), nil
}

func postIntegrationIdentity(id, preferredLocale string) *auth.Identity {
	traits := structured.Fields{"name": "Label integration admin"}
	if preferredLocale != "" {
		traits["preferred_locale"] = preferredLocale
	}
	return &auth.Identity{ID: id, ExternalID: integrationMemberID(id), State: auth.KratosStateActive, Traits: traits}
}

type labelTestRenderConfig struct{}

func (labelTestRenderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":""}`)
	digest := sha256.Sum256(payload)
	return payload, fmt.Sprintf("%x", digest[:]), nil
}

func newOGRefresherForTest(db *gorm.DB, cdnDomain string) *og.Refresher {
	planner := og.NewPlanner(db, cdnDomain, labelTestRenderConfig{}, labeladapter.NewProjection())
	return og.NewRefresher(planner, og.NewResolver(labeladapter.NewRequests()))
}

func newLabelRuntimeForTest(db *gorm.DB, cdnDomain string) Runtime {
	return labeladapter.NewRuntime(cdnDomain, newOGRefresherForTest(db, cdnDomain))
}

func newPageIntegrationContentBlockStore(t *testing.T, spiceDB *auth.SpiceDBClient) *contentblock.Store {
	t.Helper()
	return testutil.NewEmailContentBlockStore(t, spiceDB)
}

func newCreativeContentIntegrationStore(t *testing.T, spiceDB *auth.SpiceDBClient) *contentblock.Store {
	return newPageIntegrationContentBlockStore(t, spiceDB)
}

func creativeContentIntegrationDocument(sourceLocale, text string) *contentv1.RichTextDocument {
	blockID := uuid.NewString()
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, SourceLocale: sourceLocale,
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block:     &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}}},
			Placement: &contentv1.ContentBlockPlacement{Index: 0},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{Locale: sourceLocale, Blocks: []*contentv1.RichTextBlockLocale{{
			BlockId: blockID, Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
				Props: &contentv1.ParagraphLocaleProps{}, Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: text}}}},
			}},
		}}}},
	}
}

func attachCreativeContentIntegrationDocument(t *testing.T, db *gorm.DB, store *contentblock.Store, entityType, entityID, sourceLocale, text string) string {
	t.Helper()
	var revision string
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		created, err := store.CreateDocument(t.Context(), tx, contentblock.CreateInput{Profile: creativeContentProfile, SourceLocale: sourceLocale})
		if err != nil {
			return err
		}
		if err := tx.Table(entityType).Where("id = ?::uuid", entityID).Update("content_document_id", created.Document.ID).Error; err != nil {
			return err
		}
		replacement, err := contentblock.ReplaceFromRichTextProto(created.Document.ID, created.Document.Revision, creativeContentIntegrationDocument(sourceLocale, text))
		if err != nil {
			return err
		}
		result, err := store.ReplaceSnapshot(t.Context(), tx, replacement, func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
		})
		if err != nil {
			return err
		}
		revision = result.DocumentRevision.String()
		return nil
	}))
	return revision
}

func createIntegrationLabel(t *testing.T, service *LabelService, ctx context.Context, name, slug string) string {
	t.Helper()
	response, err := service.CreateLabel(ctx, connect.NewRequest(&managev1.CreateLabelRequest{Name: name, Slug: &slug, Document: creativeContentIntegrationDocument("en", name)}))
	require.NoError(t, err)
	require.NotEmpty(t, response.Msg.Id)
	return response.Msg.Id
}

func requireResourceManagerRow(t *testing.T, db *gorm.DB, tableName, resourceIDColumn, resourceID, memberID string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM `+tableName+` WHERE `+resourceIDColumn+` = ? AND member_id = ?`, resourceID, memberID).Scan(&count).Error)
	require.Equal(t, int64(1), count)
}

func requireInternalResourceAdmin(t *testing.T, spiceDB *auth.SpiceDBClient, identityID string) {
	syncArtistIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
}

func attachInternalResourcePolicy(t *testing.T, spiceDB *auth.SpiceDBClient, resourceID string) {
	t.Helper()
	mutation, err := policyv1.Label.TouchPolicy(resourceID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), mutation)
	require.NoError(t, err)
}

func seedImageBindingUploadedFileFixtureForKind(t *testing.T, db *gorm.DB, key, kind string) string {
	t.Helper()
	fileID := uuid.NewString()
	digest := sha256.Sum256([]byte(key))
	require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256) VALUES (?, ?, 'image/webp', 1024, 'webp', ?)`, fileID, fileID, digest[:]).Error)
	lifecycle := mediaasset.NewLifecycle(db, "")
	asset, _, err := lifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{SourceFileID: &fileID, Kind: kind, Extension: "webp", MimeType: "image/webp", Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE})
	require.NoError(t, err)
	assetDigest := sha256.Sum256([]byte(asset.ID))
	_, err = lifecycle.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{AssetId: asset.ID, FileSize: 1024, Sha256: assetDigest[:]})
	require.NoError(t, err)
	return fileID
}

func requireNoPublicAssetBinding(t *testing.T, db *gorm.DB, ownerType, ownerID, bindingKey string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&model.PublicAssetBinding{}).Where("owner_type = ? AND owner_id = ? AND binding_key = ?", ownerType, ownerID, bindingKey).Count(&count).Error)
	require.Zero(t, count)
}

func requirePublicAssetDeletePending(t *testing.T, db *gorm.DB, assetID string) {
	t.Helper()
	var asset model.PublicAsset
	require.NoError(t, db.Select("id", "status").Take(&asset, "id = ?", assetID).Error)
	require.Equal(t, model.PublicAssetStatusDeletePending, asset.Status)
}
