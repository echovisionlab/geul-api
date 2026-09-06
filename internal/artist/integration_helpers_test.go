//go:build integration

package artist

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
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

func normalizedUniqueStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

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

func artistIdentity(identityID string) *auth.Identity {
	return &auth.Identity{
		ID: identityID, ExternalID: artistMemberID(identityID), State: auth.KratosStateActive,
		Traits: structured.Fields{"name": "Artist integration admin", "preferred_locale": "en"},
	}
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

func (artistIntegrationTranslation) ResolveInitialSourceLocale(context.Context, *gorm.DB, auth.IdentityManager, string) string {
	return "en"
}
func (artistIntegrationTranslation) NormalizeInitialSourceLocale(context.Context, *gorm.DB, string) string {
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

type artistOGRequests struct{ localized *og.LocalizedRequests }

func newArtistOGRequests() *artistOGRequests {
	return &artistOGRequests{localized: og.NewLocalizedRequests(og.LocalizedRequestSpec{
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

func newArtistOGRefresher(db *gorm.DB, cdnDomain string) *og.Refresher {
	planner := og.NewPlanner(db, cdnDomain, artistOGRenderConfig{}, artistOGProjection{})
	return og.NewRefresher(planner, og.NewResolver(newArtistOGRequests()))
}

type artistIntegrationRuntime struct {
	cdnDomain string
	refresher *og.Refresher
}

func newArtistIntegrationRuntime(db *gorm.DB, cdnDomain string) *artistIntegrationRuntime {
	return &artistIntegrationRuntime{cdnDomain: cdnDomain, refresher: newArtistOGRefresher(db, cdnDomain)}
}

func (r *artistIntegrationRuntime) LoadArtistImages(ctx context.Context, db *gorm.DB, artistIDs []string) (map[string][]ArtistImageProjection, error) {
	result := make(map[string][]ArtistImageProjection, len(artistIDs))
	artistIDs = normalizedUniqueStrings(artistIDs)
	if len(artistIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		ArtistID    string  `gorm:"column:artist_id"`
		FileID      string  `gorm:"column:file_id"`
		SortOrder   int32   `gorm:"column:sort_order"`
		AssetID     *string `gorm:"column:asset_id"`
		Extension   *string `gorm:"column:extension"`
		MimeType    *string `gorm:"column:mime_type"`
		FileSize    *int64  `gorm:"column:file_size"`
		SHA256      []byte  `gorm:"column:sha256"`
		Disposition *string `gorm:"column:disposition"`
	}
	if err := db.WithContext(ctx).Raw(`
		SELECT relation.artist_id::text, relation.file_id::text, relation.sort_order,
		       asset.id::text AS asset_id, asset.extension, asset.mime_type,
		       asset.file_size, asset.sha256, asset.disposition
		FROM artist_file AS relation
		LEFT JOIN LATERAL (
			SELECT id, extension, mime_type, file_size, sha256, disposition
			FROM public_asset
			WHERE source_file_id = relation.file_id AND kind = 'image' AND status = 'ready'
			ORDER BY created_at DESC, id DESC LIMIT 1
		) AS asset ON TRUE
		WHERE relation.artist_id IN ?
		ORDER BY relation.artist_id, relation.sort_order, relation.file_id
	`, artistIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	lifecycle := mediaasset.NewLifecycle(db, r.cdnDomain)
	for _, row := range rows {
		projection := ArtistImageProjection{ArtistID: row.ArtistID, FileID: row.FileID, SortOrder: row.SortOrder}
		if row.AssetID != nil && row.Extension != nil && row.MimeType != nil && row.FileSize != nil && row.Disposition != nil {
			asset, err := lifecycle.AssetRef(model.PublicAsset{
				ID: *row.AssetID, Kind: "image", Extension: *row.Extension, MimeType: *row.MimeType,
				FileSize: row.FileSize, SHA256: row.SHA256, Disposition: *row.Disposition,
			})
			if err != nil {
				return nil, err
			}
			projection.Asset = asset
		}
		result[row.ArtistID] = append(result[row.ArtistID], projection)
	}
	return result, nil
}

func (r *artistIntegrationRuntime) ResolveReadyAssetRefs(ctx context.Context, db *gorm.DB, ids []string) (map[string]*commonv1.AssetRef, error) {
	ids = normalizedUniqueStrings(ids)
	result := make(map[string]*commonv1.AssetRef, len(ids))
	var assets []model.PublicAsset
	if len(ids) > 0 {
		if err := db.WithContext(ctx).Where("id IN ? AND status = ?", ids, model.PublicAssetStatusReady).Find(&assets).Error; err != nil {
			return nil, err
		}
	}
	lifecycle := mediaasset.NewLifecycle(db, r.cdnDomain)
	for _, asset := range assets {
		if asset.FileSize == nil || len(asset.SHA256) != 32 {
			continue
		}
		ref, err := lifecycle.AssetRef(asset)
		if err != nil {
			return nil, err
		}
		result[asset.ID] = ref
	}
	return result, nil
}

func (r *artistIntegrationRuntime) ReplaceArtistImageBindings(ctx context.Context, tx *gorm.DB, artistID string, fileIDs []string) error {
	if err := mediaasset.LockAttachableFilesForUpdate(ctx, tx, fileIDs); err != nil {
		return err
	}
	var current []string
	if err := tx.Model(&model.PublicAssetBinding{}).Where("owner_type = ? AND owner_id = ?", "artist", artistID).
		Where("binding_key = ? OR binding_key LIKE ?", "image", "image:%").Pluck("binding_key", &current).Error; err != nil {
		return err
	}
	if err := tx.Where("artist_id = ?", artistID).Delete(&model.ArtistFile{}).Error; err != nil {
		return err
	}
	lifecycle := mediaasset.NewLifecycle(tx, r.cdnDomain)
	next := make(map[string]struct{}, len(fileIDs))
	for index, fileID := range fileIDs {
		if err := tx.Create(&model.ArtistFile{ArtistID: artistID, FileID: fileID, SortOrder: index}).Error; err != nil {
			return err
		}
		asset, err := lifecycle.ReadyAssetRefForSourceFile(ctx, fileID, "image")
		if err != nil {
			return err
		}
		key := "image:" + fileID
		next[key] = struct{}{}
		if err := lifecycle.BindPublicAsset(ctx, mediaasset.Binding{AssetID: asset.GetAssetId(), OwnerType: "artist", OwnerID: artistID, BindingKey: key, SourceFileID: &fileID}); err != nil {
			return err
		}
	}
	removed := make([]string, 0, len(current))
	for _, key := range current {
		if _, ok := next[key]; !ok {
			removed = append(removed, key)
		}
	}
	return lifecycle.ReleaseExactPublicAssetBindings(ctx, "artist", artistID, removed)
}

func (r *artistIntegrationRuntime) RequestCurrentWithDB(ctx context.Context, tx *gorm.DB, entityType managev1.OgEntityType, entityID, locale string, allLocales bool, reason string) (string, error) {
	plan, err := r.refresher.RequestCurrentWithDB(ctx, tx, entityType, entityID, locale, allLocales, reason)
	if err != nil || plan == nil {
		return "", err
	}
	return plan.RunID, nil
}

func (r *artistIntegrationRuntime) CancelAndReleaseEntityWithDB(ctx context.Context, tx *gorm.DB, entityType managev1.OgEntityType, ownerType, ownerID string) error {
	if err := og.NewLifecycle(tx, r.cdnDomain).CancelEntityWithDB(ctx, tx, entityType, ownerID); err != nil {
		return err
	}
	return r.ReleasePublicAssetBindings(ctx, tx, ownerType, ownerID, "og")
}

func (r *artistIntegrationRuntime) ReleasePublicAssetBindings(ctx context.Context, tx *gorm.DB, ownerType, ownerID, prefix string) error {
	return mediaasset.NewLifecycle(tx, r.cdnDomain).ReleasePublicAssetBindings(ctx, ownerType, ownerID, prefix)
}

func (*artistIntegrationRuntime) EnsureResourceRouteAvailable(ctx context.Context, tx *gorm.DB, resourceType, prefix, slug string) error {
	return routeregistry.EnsureResourceRouteAvailable(ctx, tx, resourceType, prefix, slug)
}

func (*artistIntegrationRuntime) EnsureResourceRouteAvailableInTx(ctx context.Context, tx *gorm.DB, resourceType, prefix, slug string) error {
	return routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, resourceType, prefix, slug)
}

func (*artistIntegrationRuntime) IsResourceRouteAvailable(ctx context.Context, db *gorm.DB, prefix, slug string) (bool, error) {
	return routeregistry.IsResourceRouteAvailable(ctx, db, prefix, slug)
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

var _ Translation = artistIntegrationTranslation{}
var _ auth.IdentityManager = artistIdentityManager{}
