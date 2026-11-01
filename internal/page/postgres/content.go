package pagepostgres

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/page"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// domainFence runs the Page's fence over the transaction the content store
// hands it.
func (s *store) domainFence(fence page.ContentFence) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		return fence(ctx, s.with(tx), documentID)
	}
}

func (s *store) metadataEffect(effect page.MetadataEffect) func(context.Context, *gorm.DB) (contentblock.MetadataEffect, error) {
	return func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
		return effect(ctx, s.with(tx))
	}
}

func (s *store) CreateContentDocument(ctx context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error) {
	return s.ports.ContentBlocks.CreateDocument(ctx, s.tx, input)
}

func (s *store) LockedContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.ports.ContentBlocks.LoadSnapshotInTransaction(ctx, s.tx, documentID, sourceLocale)
}

func (s *store) ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.ports.ContentBlocks.LoadSnapshot(ctx, s.tx, documentID, sourceLocale)
}

func (s *store) ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence page.ContentFence) (contentblock.Result, error) {
	return s.ports.ContentBlocks.ApplyBatch(ctx, s.tx, batch, s.domainFence(fence))
}

func (s *store) ApplyContentBatchWithMetadata(ctx context.Context, batch contentblock.Batch, fence page.ContentFence, effect page.MetadataEffect) (contentblock.Result, error) {
	return s.ports.ContentBlocks.ApplyBatchWithMetadata(ctx, s.tx, batch, s.domainFence(fence), s.metadataEffect(effect))
}

func (s *store) ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence page.ContentFence, effect page.TargetMetadataEffect) (contentblock.Result, error) {
	return s.ports.ContentBlocks.ApplyTargetLocaleBatchWithMetadata(ctx, s.tx, batch, locale, s.domainFence(fence),
		func(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
			return effect(ctx, s.with(tx), contentChanged)
		})
}

func (s *store) ReplaceContentSnapshot(ctx context.Context, replace contentblock.ReplaceInput, fence page.ContentFence) (contentblock.Result, error) {
	return s.ports.ContentBlocks.ReplaceSnapshot(ctx, s.tx, replace, s.domainFence(fence))
}

func (s *store) AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence page.ContentFence, effect page.MetadataEffect) (contentblock.AdvanceResult, error) {
	return s.ports.ContentBlocks.AdvanceRevision(ctx, s.tx, input, s.domainFence(fence), s.metadataEffect(effect))
}

func (s *store) DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence page.ContentFence) error {
	return s.ports.ContentBlocks.DeleteDocument(ctx, s.tx, documentID, s.domainFence(fence))
}

func (s *store) LocaleOwnershipConflict(ctx context.Context, documentID uuid.UUID, message string) error {
	return contentblock.LocaleOwnershipConflict(ctx, s.tx, documentID, message)
}

func (s *store) PublicationAttachments(ctx context.Context, documentID uuid.UUID) ([]contentblock.PublicationAttachment, error) {
	return s.ports.ContentBlocks.LoadPublicationAttachments(ctx, s.tx, documentID)
}

func (s *store) MediaReferences(ctx context.Context, documentID uuid.UUID) ([]*contentv1.ContentBlockMediaItem, error) {
	return contentblock.LoadMediaReferences(ctx, s.tx, documentID)
}

func (s *store) LockAttachableFiles(ctx context.Context, fileIDs []string) error {
	return s.ports.Runtime.LockAttachableFilesForUpdate(ctx, s.tx, fileIDs)
}

func (s *store) FileExists(ctx context.Context, fileID string) error {
	var file model.File
	return notFound(s.tx.WithContext(ctx).First(&file, "id = ?", fileID).Error, page.ErrFileNotFound)
}

func (s *store) PublicationFiles(ctx context.Context, fileIDs []string) ([]page.PublicationFile, error) {
	var rows []struct {
		ID                string     `gorm:"column:id"`
		MIMEType          string     `gorm:"column:mime_type"`
		DurationSeconds   *int       `gorm:"column:duration_seconds"`
		DeleteRequestedAt *time.Time `gorm:"column:delete_requested_at"`
	}
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Table("file").Select("id, mime_type, duration_seconds, delete_requested_at").
		Where("id IN ?", fileIDs).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	files := make([]page.PublicationFile, len(rows))
	for i, row := range rows {
		files[i] = page.PublicationFile{
			ID: row.ID, MIMEType: row.MIMEType, DurationSeconds: row.DurationSeconds, DeleteRequested: row.DeleteRequestedAt != nil,
		}
	}
	return files, nil
}

type publicationDerivativeRow struct {
	FileID                  string  `gorm:"column:file_id"`
	Type                    string  `gorm:"column:type"`
	AssetStatus             *string `gorm:"column:asset_status"`
	AssetFileSize           *int64  `gorm:"column:asset_file_size"`
	AssetSHA256             []byte  `gorm:"column:asset_sha256"`
	MediaGenerationFileID   *string `gorm:"column:media_generation_file_id"`
	MediaGenerationStatus   *string `gorm:"column:media_generation_status"`
	MediaGenerationManifest *string `gorm:"column:media_generation_manifest"`
}

func (s *store) PublicationPresentations(ctx context.Context, fileIDs []string) (page.Presentations, error) {
	var sourceAssets []struct {
		SourceFileID string `gorm:"column:source_file_id"`
		Kind         string `gorm:"column:kind"`
		FileSize     *int64 `gorm:"column:file_size"`
		SHA256       []byte `gorm:"column:sha256"`
	}
	if err := s.tx.WithContext(ctx).Table("public_asset").
		Select("source_file_id, kind, file_size, sha256").
		Where("source_file_id IN ? AND kind IN ? AND status = ?", fileIDs, []string{"image", "mesh"}, model.PublicAssetStatusReady).
		Find(&sourceAssets).Error; err != nil {
		return page.Presentations{}, err
	}
	presentations := page.Presentations{SourceAssets: make(map[page.PresentationKey]bool, len(sourceAssets))}
	for _, asset := range sourceAssets {
		if asset.FileSize != nil && len(asset.SHA256) == 32 {
			presentations.SourceAssets[page.PresentationKey{FileID: asset.SourceFileID, Kind: asset.Kind}] = true
		}
	}
	var derivatives []publicationDerivativeRow
	if err := s.tx.WithContext(ctx).Table("file_derivative AS fd").Select(`
		fd.file_id, fd.type, pa.status AS asset_status, pa.file_size AS asset_file_size,
		pa.sha256 AS asset_sha256, mg.file_id AS media_generation_file_id,
		mg.status AS media_generation_status, mg.manifest_name AS media_generation_manifest`).
		Joins("LEFT JOIN public_asset AS pa ON pa.id = fd.asset_id").
		Joins("LEFT JOIN media_generation AS mg ON mg.id = fd.media_generation_id").
		Where("fd.file_id IN ?", fileIDs).Find(&derivatives).Error; err != nil {
		return page.Presentations{}, err
	}
	presentations.Derivatives = make(map[page.PresentationKey]bool, len(derivatives))
	for _, derivative := range derivatives {
		presentations.Derivatives[page.PresentationKey{FileID: derivative.FileID, Kind: derivative.Type}] = derivative.ready()
	}
	return presentations, nil
}

// ready reports whether the derivative can be delivered: an HLS stream needs
// its ready manifest, any other derivative its ready asset.
func (d publicationDerivativeRow) ready() bool {
	if d.Type == managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String() {
		return d.MediaGenerationFileID != nil && *d.MediaGenerationFileID == d.FileID &&
			d.MediaGenerationStatus != nil && *d.MediaGenerationStatus == model.MediaGenerationStatusReady &&
			d.MediaGenerationManifest != nil && strings.TrimSpace(*d.MediaGenerationManifest) != ""
	}
	return d.AssetStatus != nil && *d.AssetStatus == model.PublicAssetStatusReady &&
		d.AssetFileSize != nil && len(d.AssetSHA256) == 32
}

func (s *store) ReleaseFeaturedImageBindings(ctx context.Context, pageID string) error {
	return s.ports.Runtime.ReleasePublicAssetBindings(ctx, s.tx, "page", pageID, "featured_image")
}

func (s *store) ReleaseExactFeaturedImageBinding(ctx context.Context, pageID string) error {
	return s.ports.Runtime.ReleaseExactPublicAssetBindings(ctx, s.tx, "page", pageID, []string{"featured_image"})
}

func (s *store) ReadyAssets(ctx context.Context, assetIDs []string) (map[string]*commonv1.AssetRef, error) {
	return s.ports.Runtime.ResolveReadyAssetRefs(ctx, s.tx, assetIDs)
}

func (s *store) UnavailableAttachments(ctx context.Context, document *contentv1.LocalizedPageDocument) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error) {
	return s.ports.Runtime.LoadUnavailableVersionAttachmentKinds(ctx, s.tx, document)
}

func (s *store) HydrateBlockMedia(ctx context.Context, pageID string, documentID uuid.UUID, principal *auth.UserInfo, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
	return s.ports.MediaHydrator.HydrateAuthorizedPageBlockMediaWithDB(ctx, s.tx, pageID, documentID, principal, items)
}

func (s *store) RequestOG(ctx context.Context, pageID, locale string, allLocales bool, reason string) (string, error) {
	return s.ports.Runtime.RequestCurrentWithDB(ctx, s.tx, managev1.OgEntityType_OG_ENTITY_TYPE_PAGE, pageID, locale, allLocales, reason)
}

func (s *store) CancelOG(ctx context.Context, pageID string) error {
	return s.ports.Runtime.CancelAndReleaseEntityWithDB(ctx, s.tx, managev1.OgEntityType_OG_ENTITY_TYPE_PAGE, "page", pageID)
}

func (s *store) ResolveSessionPrincipal(ctx context.Context, sessionID string) (*auth.UserInfo, error) {
	return auth.ResolveAuthenticatedPrincipalBySessionID(ctx, s.tx, sessionID)
}

func (s *store) LockSession(ctx context.Context, sessionID string, principal *auth.UserInfo) error {
	return auth.LockAuthenticatedSessionForPrincipal(ctx, s.tx, sessionID, principal)
}

func (s *store) LockMembers(ctx context.Context, memberIDs []string) ([]string, error) {
	return s.ports.Members.LockMembers(ctx, s.tx, memberIDs)
}

func (s *store) ActiveIdentities(ctx context.Context, memberIDs []string) (map[string]string, error) {
	return s.ports.Members.ActiveIdentities(ctx, s.tx, memberIDs)
}

func (s *store) Nicknames(ctx context.Context, memberIDs []string) (map[string]string, error) {
	return s.ports.Members.Nicknames(ctx, s.tx, memberIDs)
}

func (s *store) PreferredLocale(ctx context.Context, memberID string) (*string, error) {
	return s.ports.Members.PreferredLocale(ctx, s.tx, memberID)
}

func (s *store) LockUserTags(ctx context.Context, tagIDs []string) ([]string, error) {
	return s.ports.Members.LockUserTags(ctx, s.tx, tagIDs)
}

func (s *store) DeleteShareLinks(ctx context.Context, pageID string) error {
	return s.ports.ShareLinks.DeletePageShareLinks(ctx, s.tx, pageID)
}

func (s *store) UpdateMenuTargets(ctx context.Context, pageID, previousSlug, next string) error {
	return s.ports.MenuTargets.UpdateSlug(ctx, s.tx, "page", pageID, previousSlug, next)
}

func (s *store) RemoveMenuTargets(ctx context.Context, pageID, slug string) error {
	return s.ports.MenuTargets.Remove(ctx, s.tx, "page", pageID, slug)
}
