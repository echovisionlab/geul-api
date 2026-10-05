package label

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	labelpublic "github.com/echovisionlab/geul-api/internal/label/public"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PublicRuntime adapts shared persistence, authorization, and immutable media
// projection to the Label public read boundary.
type PublicRuntime struct {
	db            *gorm.DB
	cdnDomain     string
	spiceDB       *auth.SpiceDBClient
	contentBlocks *contentblock.Store
}

func NewPublicRuntime(
	db *gorm.DB,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
	contentBlocks *contentblock.Store,
) *PublicRuntime {
	if db == nil || spiceDB == nil || contentBlocks == nil {
		panic("Label public DB, SpiceDB, and content Block dependencies are required")
	}
	return &PublicRuntime{
		db: db, cdnDomain: strings.TrimRight(strings.TrimSpace(cdnDomain), "/"),
		spiceDB: spiceDB, contentBlocks: contentBlocks,
	}
}

func (runtime *PublicRuntime) LoadSourceTitles(ctx context.Context, tx *gorm.DB, labelIDs []string) (map[string]string, error) {
	titles := make(map[string]string, len(labelIDs))
	if len(labelIDs) == 0 {
		return titles, nil
	}
	var rows []struct {
		EntityID string `gorm:"column:entity_id"`
		Title    string `gorm:"column:title"`
	}
	if err := tx.WithContext(ctx).Table("label_translation AS localized").
		Select("localized.entity_id, localized.title").
		Joins(`JOIN label AS source
		       ON source.id = localized.entity_id
		      AND source.source_locale = localized.locale`).
		Where("localized.entity_id IN ?", labelIDs).Scan(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	for _, row := range rows {
		titles[row.EntityID] = row.Title
	}
	for _, labelID := range labelIDs {
		if _, ok := titles[labelID]; !ok {
			return nil, errs.NotFound("label_translation", labelID)
		}
	}
	return titles, nil
}

func (runtime *PublicRuntime) LoadDocument(ctx context.Context, tx *gorm.DB, labelID, locale string) (labelpublic.ContentDocument, error) {
	var root struct {
		DocumentID *uuid.UUID `gorm:"column:content_document_id"`
	}
	if err := tx.WithContext(ctx).Table("label").Clauses(clause.Locking{Strength: "SHARE"}).Select("content_document_id").Where("id = ?", labelID).Take(&root).Error; err != nil {
		return labelpublic.ContentDocument{}, err
	}
	if root.DocumentID == nil || *root.DocumentID == uuid.Nil {
		return labelpublic.ContentDocument{}, errs.FailedPrecondition("label content document is not initialized")
	}
	var source struct {
		Locale string `gorm:"column:source_locale"`
	}
	if err := tx.WithContext(ctx).Table("label").Clauses(clause.Locking{Strength: "SHARE"}).
		Select("source_locale").Where("id = ?", labelID).Take(&source).Error; err != nil {
		return labelpublic.ContentDocument{}, err
	}
	snapshot, err := runtime.contentBlocks.LoadSnapshotInTransaction(ctx, tx, *root.DocumentID, source.Locale)
	if err != nil {
		return labelpublic.ContentDocument{}, err
	}
	if snapshot.SourceLocale != source.Locale {
		return labelpublic.ContentDocument{}, errs.Internal(errors.New("label content document source locale does not match translation authority"))
	}
	selectedLocale := strings.TrimSpace(locale)
	if selectedLocale == "" {
		selectedLocale = source.Locale
	} else if normalized := localization.NormalizeExactSupportedLocale(selectedLocale); normalized != nil {
		selectedLocale = *normalized
	} else {
		return labelpublic.ContentDocument{}, errs.InvalidArgument("locale", "must be an exact canonical locale")
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, selectedLocale)
	if err != nil {
		return labelpublic.ContentDocument{}, err
	}
	var sourceMetadata struct {
		Title string `gorm:"column:title"`
	}
	if err := tx.WithContext(ctx).Table("label_translation").Select("title").Where("entity_id = ? AND locale = ?", labelID, source.Locale).Take(&sourceMetadata).Error; err != nil {
		return labelpublic.ContentDocument{}, err
	}
	return labelpublic.ContentDocument{Document: document, Revision: snapshot.Document.Revision.String(), SourceTitle: sourceMetadata.Title}, nil
}

func (runtime *PublicRuntime) Require(ctx context.Context, labelID, token, password string) error {
	user := auth.GetUser(ctx)
	if user != nil && user.Authenticated && !user.Banned && strings.TrimSpace(user.IdentityID.String()) != "" {
		can, err := policyv1.Label.View(labelID)
		if err != nil {
			return errs.Internal(err)
		}
		decision, err := auth.AuthorizationDecision(ctx, can)
		if err != nil {
			return errs.Internal(err)
		}
		allowed, err := runtime.spiceDB.Can(ctx, decision)
		if err != nil {
			return errs.Internal(fmt.Errorf("check Label draft view permission: %w", err))
		}
		if allowed {
			return nil
		}
	}
	if strings.TrimSpace(token) == "" {
		return errs.NotFoundMsg("label not found")
	}
	_, err := sharelink.ValidateForEntity(ctx, runtime.db, token, password, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_LABEL, labelID)
	if err != nil {
		return errs.NotFoundMsg("label not found")
	}
	return nil
}

func (runtime *PublicRuntime) ReadyForSourceFiles(ctx context.Context, tx *gorm.DB, fileIDs []string, kinds ...string) (map[string]*commonv1.AssetRef, error) {
	ids := normalizedIDs(fileIDs)
	refs := make(map[string]*commonv1.AssetRef, len(ids))
	if len(ids) == 0 {
		return refs, nil
	}
	query := tx.WithContext(ctx).Where("source_file_id IN ? AND status = ?", ids, model.PublicAssetStatusReady)
	if len(kinds) > 0 {
		query = query.Where("kind IN ?", kinds)
	}
	var assets []model.PublicAsset
	if err := query.Order("source_file_id ASC, created_at DESC, id DESC").Find(&assets).Error; err != nil {
		return nil, errs.Internal(err)
	}
	lifecycle := mediaasset.NewLifecycle(tx, runtime.cdnDomain)
	for _, asset := range assets {
		if asset.SourceFileID == nil || refs[*asset.SourceFileID] != nil || asset.FileSize == nil || len(asset.SHA256) != 32 {
			continue
		}
		ref, err := lifecycle.AssetRef(asset)
		if err != nil {
			return nil, err
		}
		refs[*asset.SourceFileID] = ref
	}
	return refs, nil
}

func (runtime *PublicRuntime) ReadyForAssetIDs(ctx context.Context, tx *gorm.DB, assetIDs []string) (map[string]*commonv1.AssetRef, error) {
	ids := normalizedIDs(assetIDs)
	refs := make(map[string]*commonv1.AssetRef, len(ids))
	if len(ids) == 0 {
		return refs, nil
	}
	var assets []model.PublicAsset
	if err := tx.WithContext(ctx).Where("id IN ? AND status = ?", ids, model.PublicAssetStatusReady).Find(&assets).Error; err != nil {
		return nil, errs.Internal(err)
	}
	lifecycle := mediaasset.NewLifecycle(tx, runtime.cdnDomain)
	for _, asset := range assets {
		if asset.FileSize == nil || len(asset.SHA256) != 32 {
			continue
		}
		ref, err := lifecycle.AssetRef(asset)
		if err != nil {
			return nil, err
		}
		refs[asset.ID] = ref
	}
	return refs, nil
}

func (runtime *PublicRuntime) ResolveOg(ctx context.Context, tx *gorm.DB, assetID *string) (*commonv1.AssetRef, error) {
	if assetID == nil || strings.TrimSpace(*assetID) == "" {
		return nil, nil
	}
	refs, err := runtime.ReadyForAssetIDs(ctx, tx, []string{*assetID})
	if err != nil {
		return nil, err
	}
	return refs[strings.TrimSpace(*assetID)], nil
}

func normalizedIDs(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

var (
	_ labelpublic.ContentProjection = (*PublicRuntime)(nil)
	_ labelpublic.DraftAccess       = (*PublicRuntime)(nil)
	_ labelpublic.MediaHydrator     = (*PublicRuntime)(nil)
)
