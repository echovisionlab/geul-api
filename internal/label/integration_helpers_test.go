//go:build integration

package label_test

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	label "github.com/echovisionlab/geul-api/internal/label"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"

	"connectrpc.com/connect"
	artistadapter "github.com/echovisionlab/geul-api/internal/adapters/artist"
	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	programeventadapter "github.com/echovisionlab/geul-api/internal/adapters/programevent"
	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	labelpostgres "github.com/echovisionlab/geul-api/internal/label/postgres"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
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

// requestBackground carries the request context a request-attributed audit
// record needs; the caller's user becomes its actor.
func requestBackground() context.Context {
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.97")
	if err != nil {
		panic(err)
	}
	return sharedtelemetry.WithRequestContext(context.Background(), requestContext)
}

// integrationDeps are the collaborators an integration test may set; the
// rest take the integration defaults.
type integrationDeps struct {
	DB            *gorm.DB
	SpiceDB       *auth.SpiceDBClient
	Audit         domainaudit.Appender
	Publisher     contentsignal.Publisher
	ContentBlocks *contentblock.Store
	Checkpoints   persistencecheckpoint.ContributorFence
	Translation   labelpostgres.Translation
	Members       label.MemberProjection
	Runtime       labelpostgres.Runtime
	Artists       labelpostgres.ArtistRelations
	Releases      labelpostgres.ReleaseRelations
	ProgramEvents labelpostgres.ProgramEventRelations
	ShareLinks    labelpostgres.ShareLinks
}

// newTestService fills the collaborators deps leaves unset with the
// integration defaults: an audit recorder, a no-op signal publisher, the
// creative content store and the real Label adapters.
func newTestService(t *testing.T, deps integrationDeps) *label.Service {
	t.Helper()
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	if deps.Publisher == nil {
		deps.Publisher = noopLabelPublisher{}
	}
	if deps.ContentBlocks == nil {
		deps.ContentBlocks = newCreativeContentIntegrationStore(t, deps.SpiceDB)
	}
	if deps.Checkpoints == nil {
		deps.Checkpoints = testcollaboration.NewCheckpoints(deps.DB, deps.SpiceDB)
	}
	if deps.Translation == nil {
		deps.Translation = labeladapter.NewTranslation()
	}
	if deps.Members == nil {
		deps.Members = labeladapter.NewMemberProjection(deps.DB, "")
	}
	if deps.Runtime == nil {
		deps.Runtime = newLabelRuntimeForTest(deps.DB, "")
	}
	if deps.Artists == nil {
		deps.Artists = artistadapter.NewLabelRelations()
	}
	if deps.Releases == nil {
		deps.Releases = releaseadapter.NewLabelRelations()
	}
	if deps.ProgramEvents == nil {
		deps.ProgramEvents = programeventadapter.NewLabelRelations()
	}
	if deps.ShareLinks == nil {
		deps.ShareLinks = sharelinkadapter.NewCatalogLinks()
	}
	units, err := labelpostgres.NewUnits(deps.DB, labelpostgres.Deps{
		ContentBlocks: deps.ContentBlocks, Publisher: deps.Publisher, Checkpoints: deps.Checkpoints,
		Translation: deps.Translation, Runtime: deps.Runtime, Artists: deps.Artists, Releases: deps.Releases,
		ProgramEvents: deps.ProgramEvents, ShareLinks: deps.ShareLinks,
	})
	require.NoError(t, err)
	service, err := label.NewService(label.Deps{
		Units: units, SpiceDB: deps.SpiceDB, Fence: identitystate.PostgresFence{}, Audit: deps.Audit, Members: deps.Members,
	})
	require.NoError(t, err)
	return service
}

func labelIntegrationAdminCtx(identityID string) context.Context {
	return auth.WithUser(requestBackground(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(testutil.PostIntegrationMemberID(identityID)),
		SessionID: auth.SessionID(testutil.IntegrationUUID()), Authenticated: true, Onboarded: true,
	})
}

func syncLabelIntegrationGlobalRole(t *testing.T, spiceDB *auth.SpiceDBClient, identityID string, role policyv1.RoleID) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, role)
	require.NoError(t, err)
}

type noopLabelPublisher struct{}

func (noopLabelPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}
func (noopLabelPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

type capturingAsyncPublisher struct{ noopLabelPublisher }

type labelTestRenderConfig struct{}

func (labelTestRenderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":""}`)
	digest := sha256.Sum256(payload)
	return payload, fmt.Sprintf("%x", digest[:]), nil
}

func newOGRefresherForTest(db *gorm.DB, cdnDomain string) *ogpostgres.Refresher {
	planner := ogpostgres.NewPlanner(cdnDomain, labelTestRenderConfig{}, labeladapter.NewProjection())
	return ogpostgres.NewRefresher(planner, ogpostgres.NewResolver(labeladapter.NewRequests()))
}

func newLabelRuntimeForTest(db *gorm.DB, cdnDomain string) labelpostgres.Runtime {
	return labeladapter.NewRuntime(cdnDomain, newOGRefresherForTest(db, cdnDomain))
}

func newCreativeContentIntegrationStore(t *testing.T, spiceDB *auth.SpiceDBClient) *contentblock.Store {
	t.Helper()
	return testutil.NewEmailContentBlockStore(t, spiceDB)
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
		created, err := store.CreateDocument(t.Context(), tx, contentblock.CreateInput{Profile: "compact", SourceLocale: sourceLocale})
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

func createIntegrationLabel(t *testing.T, service *label.ConnectHandler, ctx context.Context, name, slug string) string {
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
	syncLabelIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
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
	lifecycle := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(db), "")
	asset, _, err := mediaasset.AllocationResultProto(lifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{SourceFileID: &fileID, Kind: kind, Extension: "webp", MimeType: "image/webp", Disposition: mediaasset.DispositionInline}))
	require.NoError(t, err)
	assetDigest := sha256.Sum256([]byte(asset.ID))
	_, err = lifecycle.CompletePublicAsset(t.Context(), mediaasset.WrittenAssetFromProto(&commonv1.AssetWriteResult{AssetId: asset.ID, FileSize: 1024, Sha256: assetDigest[:]}))
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

func (publisher noopLabelPublisher) NotifyProtobufWithExecutor(
	ctx context.Context,
	_ eventpkg.DBTX,
	signal string,
	message proto.Message,
) error {
	return publisher.NotifyProtobuf(ctx, signal, message)
}

// seedLabelLocaleTitle stores a Label locale title as the source title save
// does.
func seedLabelLocaleTitle(ctx context.Context, db *gorm.DB, labelID, locale, title string, now time.Time) error {
	return db.WithContext(ctx).Exec(`
		INSERT INTO label_translation (entity_id, locale, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET title = EXCLUDED.title, updated_at = EXCLUDED.updated_at`,
		labelID, locale, title, now, now).Error
}
func labelTargetMemberContext(t *testing.T, identityID string) context.Context {
	t.Helper()
	base := labelIntegrationAdminCtx(identityID)
	user := auth.GetUser(base)
	require.NotNil(t, user)
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(
		uuid.NewString(),
		sharedtelemetry.MemberActor{
			IdentityID: identityID,
			MemberID:   testutil.PostIntegrationMemberID(identityID),
			SessionID:  uuid.NewString(),
		},
	)
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(base, requestContext), user)
}

func seedInternalLabel(t *testing.T, db *gorm.DB, contentBlocks *contentblock.Store, ownerID string) string {
	t.Helper()

	labelID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		document, err := contentBlocks.CreateDocument(t.Context(), tx, contentblock.CreateInput{
			Profile: "compact", SourceLocale: "en",
		})
		if err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO label (id, status, content_document_id, created_at, updated_at)
			VALUES (?, 'LABEL_STATUS_DRAFT', ?, ?, ?)
		`, labelID, document.Document.ID, now, now).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO label_owner (label_id, member_id, created_at)
			VALUES (?, ?, ?)
		`, labelID, ownerID, now).Error
	}))
	return labelID
}
