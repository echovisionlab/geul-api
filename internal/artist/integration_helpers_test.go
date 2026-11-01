//go:build integration

package artist_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	artistpostgres "github.com/echovisionlab/geul-api/internal/artist/postgres"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	flag.Parse()
	code := 1
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "start Artist integration suite: %v\n", err)
		os.Exit(code)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code = m.Run()
	if err := testutil.RunIntegrationSuiteCleanups(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "cleanup Artist integration suite: %v\n", err)
		code = 1
	}
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "close Artist integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func artistMemberID(identityID string) string {
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("dsub-artist-integration-member:"+identityID))
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}

func seedArtistIdentity(t *testing.T, db *gorm.DB, identityID, name string) string {
	t.Helper()
	email := identityID + "@artist.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: email, Name: name,
	})
	memberID := artistMemberID(identityID)
	now := time.Now().UTC()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid`, memberID, identityID,
		).Error; err != nil {
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

type artistIdentityManager struct{ identity *auth.Identity }

type artistIntegrationMemberProjection struct{ db *gorm.DB }

func (p artistIntegrationMemberProjection) LoadMemberSummaries(
	ctx context.Context,
	memberIDs []string,
) (map[string]*commonv1.MemberSummary, error) {
	result := make(map[string]*commonv1.MemberSummary, len(memberIDs))
	if len(memberIDs) == 0 {
		return result, nil
	}
	var rows []model.Member
	if err := p.db.WithContext(ctx).Where("id IN ?", memberIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ID] = &commonv1.MemberSummary{
			Id: row.ID, Nickname: row.Nickname,
			Deleted: row.DeletedAt != nil || row.AccountIdentityID == nil,
		}
	}
	return result, nil
}

func (p artistIntegrationMemberProjection) LoadAuthorizationEligibleMemberSummary(
	ctx context.Context,
	memberID string,
) (*commonv1.MemberSummary, error) {
	summaries, err := p.LoadMemberSummaries(ctx, []string{memberID})
	if err != nil {
		return nil, err
	}
	summary := summaries[memberID]
	if summary == nil || summary.Deleted {
		return nil, fmt.Errorf("Artist integration Member %s is not eligible", memberID)
	}
	return summary, nil
}

func (p artistIntegrationMemberProjection) LoadAuthorizationEligibleIdentities(ctx context.Context, memberIDs []string) (map[string]string, error) {
	return memberadapter.NewParticipantIdentities(p.db).LoadAuthorizationEligibleIdentities(ctx, memberIDs)
}

func (m artistIdentityManager) GetIdentity(_ context.Context, id string) (*auth.Identity, error) {
	if m.identity == nil || m.identity.ID != id {
		return nil, errors.New("Artist integration identity not found")
	}
	return m.identity, nil
}
func (m artistIdentityManager) GetIdentityWithIncludeCredential(ctx context.Context, id, _ string) (*auth.Identity, error) {
	return m.GetIdentity(ctx, id)
}
func (artistIdentityManager) ListIdentities(context.Context, int, int) ([]*auth.Identity, int64, error) {
	return nil, 0, nil
}
func (m artistIdentityManager) GetIdentityEmail(context.Context, string) (string, error) {
	if m.identity == nil {
		return "", nil
	}
	return m.identity.CurrentEmail(), nil
}
func (artistIdentityManager) UpdateIdentityTraits(context.Context, string, structured.Fields) error {
	return nil
}
func (artistIdentityManager) UpdateIdentityVerifiableAddresses(context.Context, string, []auth.VerifiableAddress) error {
	return nil
}
func (artistIdentityManager) UpdateIdentityMetadataAdmin(context.Context, string, structured.Fields) error {
	return nil
}
func (artistIdentityManager) SetIdentityState(context.Context, string, string) error { return nil }
func (artistIdentityManager) DeleteIdentitySessions(context.Context, string) error   { return nil }
func (artistIdentityManager) DeleteIdentity(context.Context, string) error           { return nil }

type artistIntegrationTranslation struct{}

func (artistIntegrationTranslation) ResolveInitialSourceLocale(context.Context, *gorm.DB, string) string {
	return "en"
}
func (artistIntegrationTranslation) RequireDocumentContributors(ctx context.Context, tx *gorm.DB, contributors []string) error {
	if len(contributors) == 0 {
		return errors.New("Artist contributors are required")
	}
	var count int64
	if err := tx.WithContext(ctx).Table("member").Where("id IN ?", contributors).Count(&count).Error; err != nil {
		return err
	}
	if count != int64(len(contributors)) {
		return errors.New("Artist contributor does not exist")
	}
	return nil
}

type artistOGRenderConfig struct{}

func (artistOGRenderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":""}`)
	digest := sha256.Sum256(payload)
	return payload, fmt.Sprintf("%x", digest[:]), nil
}

type artistOGProjection struct{}

func (artistOGProjection) Handles(target og.Target) bool { return target.EntityType == "artist" }
func (artistOGProjection) ReleasePending(context.Context, *gorm.DB, og.Target, string) error {
	return nil
}
func (artistOGProjection) Complete(context.Context, *gorm.DB, og.Target, string, time.Time, string) error {
	return nil
}

type artistOGRequests struct{ localized *ogpostgres.LocalizedRequests }

func newArtistOGRequests() *artistOGRequests {
	return &artistOGRequests{localized: ogpostgres.NewLocalizedRequests(ogpostgres.LocalizedRequestSpec{
		EntityType: "artist", Table: "artist", TranslationTable: "artist_translation",
		SourceTitleExpression: `COALESCE((SELECT translation.title FROM artist_translation AS translation
			JOIN artist AS source ON source.id = translation.entity_id
				AND source.source_locale = translation.locale
			WHERE translation.entity_id = artist.id LIMIT 1), '')`,
		FeaturedImageExpression: `(SELECT artist_file.file_id FROM artist_file
			WHERE artist_file.artist_id = artist.id ORDER BY artist_file.sort_order ASC LIMIT 1)`,
	})}
}
func (*artistOGRequests) Handles(entityType string) bool { return entityType == "artist" }
func (r *artistOGRequests) Resolve(ctx context.Context, db *gorm.DB, _ string, entityID string, selection *managev1.OgTargetSelection) ([]og.Request, error) {
	return r.localized.Resolve(ctx, db, entityID, selection)
}

func newArtistOGRefresher(db *gorm.DB, cdnDomain string) *ogpostgres.Refresher {
	planner := ogpostgres.NewPlanner(cdnDomain, artistOGRenderConfig{}, artistOGProjection{})
	return ogpostgres.NewRefresher(planner, ogpostgres.NewResolver(newArtistOGRequests()))
}

func artistContentDocument(sourceLocale, text string) *contentv1.RichTextDocument {
	blockID := uuid.NewString()
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, SourceLocale: sourceLocale,
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{
				Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
			}}, Placement: &contentv1.ContentBlockPlacement{Index: 0},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{Locale: sourceLocale, Blocks: []*contentv1.RichTextBlockLocale{{
			BlockId: blockID, Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
				Props: &contentv1.ParagraphLocaleProps{}, Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
					Text: &contentv1.RichTextStyledText{Text: text},
				}}},
			}},
		}}}},
	}
}

func attachArtistContentDocument(t *testing.T, db *gorm.DB, store *contentblock.Store, artistID, sourceLocale, text string) string {
	t.Helper()
	var revision string
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		created, err := store.CreateDocument(t.Context(), tx, contentblock.CreateInput{Profile: "compact", SourceLocale: sourceLocale})
		if err != nil {
			return err
		}
		if err := tx.Table("artist").Where("id = ?::uuid", artistID).Update("content_document_id", created.Document.ID).Error; err != nil {
			return err
		}
		replacement, err := contentblock.ReplaceFromRichTextProto(created.Document.ID, created.Document.Revision, artistContentDocument(sourceLocale, text))
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

func artistDocumentContributorContext(t *testing.T) context.Context {
	t.Helper()
	request, err := sharedtelemetry.NewPublicRequestContext("192.0.2.91")
	require.NoError(t, err)
	return sharedtelemetry.WithRequestContext(t.Context(), request)
}

func seedArtistImageFile(t *testing.T, db *gorm.DB, fileID, fileName string) {
	t.Helper()
	digest := sha256.Sum256([]byte(fileID))
	require.NoError(t, db.Create(&model.File{
		ID: fileID, FileName: fileName, MimeType: "image/webp", FileSize: 1024,
		Extension: "webp", SHA256: digest[:],
	}).Error)
	lifecycle := mediaasset.NewLifecycle(db, "https://cdn.example.com")
	asset, _, err := lifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		SourceFileID: &fileID, Kind: "image", Extension: "webp", MimeType: "image/webp",
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	_, err = lifecycle.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{
		AssetId: asset.ID, FileSize: 1024, Sha256: digest[:],
	})
	require.NoError(t, err)
}

var _ artistpostgres.Translation = artistIntegrationTranslation{}
var _ auth.IdentityManager = artistIdentityManager{}
