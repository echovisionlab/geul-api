// Package mediaasset owns immutable public-asset and media-generation
// persistence, including the File row lock shared by attachment writers and
// deletion.
package mediaasset

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

// ErrUploadSessionNotAbortable reports that multipart completion already owns
// the upload and the caller must not delete its owning aggregate.
var ErrUploadSessionNotAbortable = errors.New("upload session is no longer abortable")

const retiredMediaGenerationRetention = 7 * time.Hour

var publicAssetKinds = map[string]struct{}{
	"image": {}, "thumbnail": {}, "waveform": {}, "spectrogram": {}, "mesh": {},
	"og": {}, "avatar": {}, "logo": {}, "favicon": {}, "loader": {},
	"gallery": {}, "artwork": {}, "poster": {}, "map_image": {}, "texture": {},
	"email_image": {},
}

// Allocation defines one immutable public asset to reserve before an external
// writer stores its bytes.
type Allocation struct {
	SourceFileID     *string
	Kind             string
	Extension        string
	MimeType         string
	Disposition      commonv1.AssetDisposition
	DownloadFilename *string
}

// Binding connects a ready public asset to the exact owner projection that
// currently exposes it.
type Binding struct {
	AssetID      string
	OwnerType    string
	OwnerID      string
	BindingKey   string
	SourceFileID *string
}

// Lifecycle coordinates public-asset and media-generation state transitions.
type Lifecycle struct {
	db        *gorm.DB
	cdnDomain string
	now       func() time.Time
}

// NewLifecycle creates a lifecycle bound to one database and CDN origin.
func NewLifecycle(db *gorm.DB, cdnDomain string) *Lifecycle {
	if db == nil {
		panic("media asset lifecycle service: db is required")
	}
	return &Lifecycle{db: db, cdnDomain: cdnDomain, now: time.Now}
}

func (s *Lifecycle) AllocatePublicAsset(
	ctx context.Context,
	input Allocation,
) (*model.PublicAsset, *commonv1.AssetWriteTarget, error) {
	input.Kind = strings.TrimSpace(input.Kind)
	input.Extension = strings.ToLower(strings.TrimSpace(input.Extension))
	input.MimeType = canonicalMimeType(input.MimeType)
	if err := validatePublicAssetAllocation(input); err != nil {
		return nil, nil, err
	}

	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, input.Extension)
	if err != nil {
		return nil, nil, errs.InvalidArgument("extension", err.Error())
	}
	now := s.now().UTC()
	asset := model.PublicAsset{
		ID:               assetID,
		SourceFileID:     normalizedOptionalString(input.SourceFileID),
		Kind:             input.Kind,
		ObjectKey:        objectKey,
		Extension:        input.Extension,
		MimeType:         input.MimeType,
		Disposition:      "inline",
		DownloadFilename: normalizedOptionalString(input.DownloadFilename),
		Status:           model.PublicAssetStatusAllocated,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	createAsset := func(tx *gorm.DB) error {
		if asset.SourceFileID != nil {
			if err := LockAttachableFilesForUpdate(ctx, tx, []string{*asset.SourceFileID}); err != nil {
				return err
			}
		}
		return tx.WithContext(ctx).Create(&asset).Error
	}
	if err := s.db.WithContext(ctx).Transaction(createAsset); err != nil {
		if _, ok := err.(*connect.Error); ok {
			return nil, nil, err
		}
		return nil, nil, errs.Internal(err)
	}
	target := &commonv1.AssetWriteTarget{
		AssetId:          asset.ID,
		ObjectKey:        asset.ObjectKey,
		Extension:        asset.Extension,
		MimeType:         asset.MimeType,
		Disposition:      input.Disposition,
		DownloadFilename: asset.DownloadFilename,
	}
	return &asset, target, nil
}

func validatePublicAssetAllocation(input Allocation) error {
	if _, ok := publicAssetKinds[input.Kind]; !ok {
		return errs.InvalidArgument("kind", "unsupported public asset kind")
	}
	if input.Extension == "" || input.MimeType == "" {
		return errs.InvalidArgument("asset", "extension and mime_type are required")
	}
	if expected := model.GetExtensionFromMime(input.MimeType); expected == "bin" || expected != input.Extension {
		return errs.InvalidArgument("extension", "does not match the verified MIME type")
	}
	if reason := PublicAssetKindMediaContractViolation(input.Kind, input.Extension, input.MimeType); reason != "" {
		return errs.InvalidArgument("asset", reason)
	}
	if input.SourceFileID != nil {
		if _, err := uuid.Parse(strings.TrimSpace(*input.SourceFileID)); err != nil {
			return errs.InvalidArgument("source_file_id", "must be a UUID")
		}
	}
	switch input.Disposition {
	case commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE:
		if normalizedOptionalString(input.DownloadFilename) != nil {
			return errs.InvalidArgument("download_filename", "is not supported for public assets")
		}
	case commonv1.AssetDisposition_ASSET_DISPOSITION_ATTACHMENT:
		return errs.InvalidArgument("disposition", "attachment public assets are not supported")
	default:
		return errs.InvalidArgument("disposition", "must be inline")
	}
	return nil
}

// PublicAssetKindMediaContractViolation reports whether a verified file shape
// can back the specified immutable public-asset kind.
func PublicAssetKindMediaContractViolation(kind string, extension string, mimeType string) string {
	kind = strings.TrimSpace(kind)
	extension = strings.ToLower(strings.TrimSpace(extension))
	mimeType = canonicalMimeType(mimeType)

	switch kind {
	case "avatar", "thumbnail":
		if extension != "webp" || mimeType != "image/webp" {
			return kind + " public assets must be verified WebP"
		}
	case "spectrogram":
		if extension != "png" || mimeType != "image/png" {
			return "spectrogram public assets must be verified PNG"
		}
	case "waveform":
		if extension != "json" || mimeType != "application/json" {
			return "waveform public assets must be verified JSON"
		}
	case "mesh":
		if extension != "glb" || mimeType != "model/gltf-binary" {
			return "mesh public assets must be verified GLB"
		}
	case "image", "og", "logo", "favicon", "loader", "gallery", "artwork", "poster", "map_image", "texture", "email_image":
		if !strings.HasPrefix(mimeType, "image/") {
			return kind + " public assets must use a verified image MIME type"
		}
	}
	return ""
}

func (s *Lifecycle) CompletePublicAsset(
	ctx context.Context,
	result *commonv1.AssetWriteResult,
) (*model.PublicAsset, error) {
	if result == nil {
		return nil, errs.Required("result")
	}
	assetID := strings.TrimSpace(result.GetAssetId())
	if _, err := uuid.Parse(assetID); err != nil {
		return nil, errs.InvalidArgument("asset_id", "must be a UUID")
	}
	if result.GetFileSize() <= 0 {
		return nil, errs.InvalidArgument("file_size", "must be positive")
	}
	if len(result.GetSha256()) != 32 {
		return nil, errs.InvalidArgument("sha256", "must be a 32-byte SHA-256 digest")
	}

	now := s.now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var asset model.PublicAsset
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", assetID).
			Take(&asset).Error; err != nil {
			return err
		}
		if asset.Status == model.PublicAssetStatusReady {
			if asset.FileSize == nil ||
				*asset.FileSize != result.GetFileSize() ||
				!bytes.Equal(asset.SHA256, result.GetSha256()) {
				return fmt.Errorf("public asset completion conflicts with ready metadata")
			}
			return nil
		}
		if asset.Status != model.PublicAssetStatusAllocated {
			return fmt.Errorf("public asset is not allocated")
		}
		return tx.Model(&model.PublicAsset{}).
			Where("id = ? AND status = ?", assetID, model.PublicAssetStatusAllocated).
			Updates(structured.Fields{
				"file_size":      result.GetFileSize(),
				"sha256":         append([]byte(nil), result.GetSha256()...),
				"status":         model.PublicAssetStatusReady,
				"ready_at":       now,
				"failure_reason": nil,
				"failed_at":      nil,
				"updated_at":     now,
			}).Error
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("public_asset", assetID)
		}
		return nil, errs.FailedPrecondition(err.Error())
	}
	return s.loadPublicAsset(ctx, assetID)
}

func (s *Lifecycle) BindPublicAsset(ctx context.Context, input Binding) error {
	if err := normalizePublicAssetBindingInput(&input); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.bindPublicAssetWithDB(ctx, tx, input)
	})
}

func normalizePublicAssetBindingInput(input *Binding) error {
	input.AssetID = strings.TrimSpace(input.AssetID)
	input.OwnerType = strings.TrimSpace(input.OwnerType)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.BindingKey = strings.TrimSpace(input.BindingKey)
	if input.AssetID == "" || input.OwnerType == "" || input.OwnerID == "" || input.BindingKey == "" {
		return errs.InvalidArgument("binding", "asset_id, owner_type, owner_id, and binding_key are required")
	}
	return nil
}

func (s *Lifecycle) bindPublicAssetWithDB(
	ctx context.Context,
	tx *gorm.DB,
	input Binding,
) error {
	existing, existingFound, err := loadPublicAssetBinding(
		ctx, tx, input.OwnerType, input.OwnerID, input.BindingKey, false,
	)
	if err != nil {
		return errs.Internal(err)
	}
	assetIDs := []string{input.AssetID}
	if existingFound {
		assetIDs = append(assetIDs, existing.AssetID)
	}
	assets, err := lockPublicAssetsForUpdate(ctx, tx, assetIDs)
	if err != nil {
		return errs.Internal(err)
	}
	asset, ok := assets[input.AssetID]
	if !ok || asset.Status != model.PublicAssetStatusReady {
		return errs.FailedPrecondition("only ready assets can be bound")
	}

	current, currentFound, err := loadPublicAssetBinding(
		ctx, tx, input.OwnerType, input.OwnerID, input.BindingKey, true,
	)
	if err != nil {
		return errs.Internal(err)
	}
	if currentFound != existingFound || (currentFound && current.AssetID != existing.AssetID) {
		return errs.FailedPrecondition("public asset binding changed concurrently; retry")
	}
	return s.storePublicAssetBinding(ctx, tx, input, current, currentFound)
}

func (s *Lifecycle) storePublicAssetBinding(
	ctx context.Context,
	tx *gorm.DB,
	input Binding,
	current model.PublicAssetBinding,
	currentFound bool,
) error {
	now := s.now().UTC()
	if !currentFound {
		binding := model.PublicAssetBinding{
			AssetID:      input.AssetID,
			OwnerType:    input.OwnerType,
			OwnerID:      input.OwnerID,
			BindingKey:   input.BindingKey,
			SourceFileID: normalizedOptionalString(input.SourceFileID),
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := tx.Create(&binding).Error; err != nil {
			if dberrors.IsUniqueViolation(err) {
				return errs.FailedPrecondition("public asset binding changed concurrently; retry")
			}
			return errs.Internal(err)
		}
		return nil
	}
	result := tx.Model(&model.PublicAssetBinding{}).
		Where(
			"owner_type = ? AND owner_id = ? AND binding_key = ? AND asset_id = ?",
			input.OwnerType,
			input.OwnerID,
			input.BindingKey,
			current.AssetID,
		).
		Updates(structured.Fields{
			"asset_id":       input.AssetID,
			"source_file_id": normalizedOptionalString(input.SourceFileID),
			"updated_at":     now,
		})
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return errs.FailedPrecondition("public asset binding changed concurrently; retry")
	}
	if current.AssetID == input.AssetID {
		return nil
	}
	if err := transitionUnboundPublicAssetsToDeletePending(ctx, tx, []string{current.AssetID}, now); err != nil {
		return errs.Internal(err)
	}
	return nil
}

func (s *Lifecycle) BindReadyAssetForSourceFile(
	ctx context.Context,
	sourceFileID string,
	ownerType string,
	ownerID string,
	bindingKey string,
	expectedKind string,
) (*commonv1.AssetRef, error) {
	sourceFileID = strings.TrimSpace(sourceFileID)
	var asset *commonv1.AssetRef
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := LockAttachableFilesForUpdate(ctx, tx, []string{sourceFileID}); err != nil {
			return err
		}
		lifecycle := NewLifecycle(tx, s.cdnDomain)
		var err error
		asset, err = lifecycle.ReadyAssetRefForSourceFile(ctx, sourceFileID, expectedKind)
		if err != nil {
			return err
		}
		return lifecycle.BindPublicAsset(ctx, Binding{
			AssetID:      asset.GetAssetId(),
			OwnerType:    ownerType,
			OwnerID:      ownerID,
			BindingKey:   bindingKey,
			SourceFileID: &sourceFileID,
		})
	})
	if err != nil {
		return nil, err
	}
	return asset, nil
}

func (s *Lifecycle) ReadyAssetRef(ctx context.Context, assetID string) (*commonv1.AssetRef, error) {
	asset, err := s.loadPublicAsset(ctx, strings.TrimSpace(assetID))
	if err != nil {
		return nil, err
	}
	if asset.Status != model.PublicAssetStatusReady || asset.FileSize == nil || len(asset.SHA256) != 32 {
		return nil, errs.FailedPrecondition("public asset is not ready")
	}
	return s.AssetRef(*asset)
}

func (s *Lifecycle) ReadyAssetRefForSourceFile(
	ctx context.Context,
	sourceFileID string,
	expectedKind string,
) (*commonv1.AssetRef, error) {
	expectedKind = strings.TrimSpace(expectedKind)
	if _, ok := publicAssetKinds[expectedKind]; !ok {
		return nil, errs.InvalidArgument("kind", "unsupported public asset kind")
	}
	var asset model.PublicAsset
	if err := s.db.WithContext(ctx).
		Where(
			"source_file_id = ? AND kind = ? AND status = ?",
			strings.TrimSpace(sourceFileID),
			expectedKind,
			model.PublicAssetStatusReady,
		).
		Order("created_at DESC").
		Take(&asset).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("public_asset_for_source_file", sourceFileID)
		}
		return nil, errs.Internal(err)
	}
	return s.AssetRef(asset)
}

func ReadyPublicAssetRefForSourceFile(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
	sourceFileID string,
	expectedKind string,
) (*commonv1.AssetRef, error) {
	return NewLifecycle(db, cdnDomain).ReadyAssetRefForSourceFile(ctx, sourceFileID, expectedKind)
}

// LoadReadyPublicAssetRefsForSourceFiles resolves the newest ready derivative
// for each source File in one query.
func LoadReadyPublicAssetRefsForSourceFiles(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
	expectedKind string,
	sourceFileIDs []string,
) (map[string]*commonv1.AssetRef, error) {
	expectedKind = strings.TrimSpace(expectedKind)
	if _, ok := publicAssetKinds[expectedKind]; !ok {
		return nil, errs.InvalidArgument("kind", "unsupported public asset kind")
	}
	sourceFileIDs = normalizedUniqueStrings(sourceFileIDs)
	refs := make(map[string]*commonv1.AssetRef, len(sourceFileIDs))
	if len(sourceFileIDs) == 0 {
		return refs, nil
	}
	var assets []model.PublicAsset
	if err := db.WithContext(ctx).
		Where("source_file_id IN ? AND kind = ? AND status = ?", sourceFileIDs, expectedKind, model.PublicAssetStatusReady).
		Order("source_file_id ASC, created_at DESC, id DESC").
		Find(&assets).Error; err != nil {
		return nil, errs.Internal(err)
	}
	lifecycle := NewLifecycle(db, cdnDomain)
	for _, asset := range assets {
		if asset.SourceFileID == nil || asset.FileSize == nil || len(asset.SHA256) != 32 {
			continue
		}
		if _, alreadyResolved := refs[*asset.SourceFileID]; alreadyResolved {
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

// AssetRef returns the immutable CDN reference for a ready public asset.
func (s *Lifecycle) AssetRef(asset model.PublicAsset) (*commonv1.AssetRef, error) {
	if asset.Kind == "attachment" || asset.Disposition != "inline" {
		return nil, errs.FailedPrecondition("attachment public assets cannot be emitted")
	}
	assetPath, err := mediaauth.AssetPath(asset.ID, asset.Kind, asset.Extension)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return &commonv1.AssetRef{
		AssetId:     asset.ID,
		Url:         joinOriginPath(s.cdnDomain, assetPath),
		Extension:   asset.Extension,
		MimeType:    asset.MimeType,
		FileSize:    *asset.FileSize,
		Sha256:      append([]byte(nil), asset.SHA256...),
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	}, nil
}

func (s *Lifecycle) RequestPublicAssetDeletion(ctx context.Context, assetID string) error {
	assetID = strings.TrimSpace(assetID)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var asset model.PublicAsset
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "status").
			Where("id = ?", assetID).
			Take(&asset).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("public_asset", assetID)
			}
			return errs.Internal(err)
		}
		switch asset.Status {
		case model.PublicAssetStatusDeletePending, model.PublicAssetStatusDeleted:
			return nil
		case model.PublicAssetStatusReady, model.PublicAssetStatusFailed:
		default:
			return errs.FailedPrecondition("public asset cannot be deleted from its current state")
		}
		bindings, err := lockPublicAssetBindingsForAssets(ctx, tx, []string{assetID})
		if err != nil {
			return errs.Internal(err)
		}
		if len(bindings) > 0 {
			return errs.FailedPrecondition("public asset still has bindings")
		}
		now := s.now().UTC()
		result := tx.Model(&model.PublicAsset{}).
			Where("id = ? AND status IN ?", assetID, []string{model.PublicAssetStatusReady, model.PublicAssetStatusFailed}).
			Updates(structured.Fields{
				"status":              model.PublicAssetStatusDeletePending,
				"delete_requested_at": now,
				"updated_at":          now,
			})
		if result.Error != nil {
			return errs.Internal(result.Error)
		}
		if result.RowsAffected != 1 {
			return errs.FailedPrecondition("public asset cannot be deleted from its current state")
		}
		return nil
	})
}

func (s *Lifecycle) ReleasePublicAssetBindings(
	ctx context.Context,
	ownerType string,
	ownerID string,
	bindingPrefix string,
) error {
	ownerType = strings.TrimSpace(ownerType)
	ownerID = strings.TrimSpace(ownerID)
	bindingPrefix = strings.TrimSpace(bindingPrefix)
	if ownerType == "" || ownerID == "" || bindingPrefix == "" {
		return errs.InvalidArgument("binding", "owner type, owner ID, and binding prefix are required")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var bindingKeys []string
		if err := tx.Model(&model.PublicAssetBinding{}).
			Where("owner_type = ? AND owner_id = ?", ownerType, ownerID).
			Where("binding_key = ? OR binding_key LIKE ?", bindingPrefix, bindingPrefix+":%").
			Order("binding_key ASC").
			Pluck("binding_key", &bindingKeys).Error; err != nil {
			return errs.Internal(err)
		}
		return releaseExactPublicAssetBindings(ctx, tx, ownerType, ownerID, bindingKeys, s.now().UTC())
	})
}

func (s *Lifecycle) ReleaseExactPublicAssetBindings(
	ctx context.Context,
	ownerType string,
	ownerID string,
	bindingKeys []string,
) error {
	ownerType = strings.TrimSpace(ownerType)
	ownerID = strings.TrimSpace(ownerID)
	bindingKeys = normalizedUniqueStrings(bindingKeys)
	if ownerType == "" || ownerID == "" {
		return errs.InvalidArgument("binding", "owner type and owner ID are required")
	}
	if len(bindingKeys) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return releaseExactPublicAssetBindings(ctx, tx, ownerType, ownerID, bindingKeys, s.now().UTC())
	})
}

func releaseExactPublicAssetBindings(
	ctx context.Context,
	tx *gorm.DB,
	ownerType string,
	ownerID string,
	bindingKeys []string,
	now time.Time,
) error {
	var snapshot []model.PublicAssetBinding
	if err := tx.WithContext(ctx).
		Where("owner_type = ? AND owner_id = ? AND binding_key IN ?", ownerType, ownerID, bindingKeys).
		Order("binding_key ASC").
		Find(&snapshot).Error; err != nil {
		return errs.Internal(err)
	}
	if len(snapshot) == 0 {
		return nil
	}

	assetIDs := make([]string, 0, len(snapshot))
	for _, binding := range snapshot {
		assetIDs = append(assetIDs, binding.AssetID)
	}
	if _, err := lockPublicAssetsForUpdate(ctx, tx, assetIDs); err != nil {
		return errs.Internal(err)
	}

	var current []model.PublicAssetBinding
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("owner_type = ? AND owner_id = ? AND binding_key IN ?", ownerType, ownerID, bindingKeys).
		Order("binding_key ASC").
		Find(&current).Error; err != nil {
		return errs.Internal(err)
	}
	if !samePublicAssetBindings(snapshot, current) {
		return errs.FailedPrecondition("public asset bindings changed concurrently; retry")
	}

	currentBindingKeys := make([]string, 0, len(current))
	for _, binding := range current {
		currentBindingKeys = append(currentBindingKeys, binding.BindingKey)
	}
	result := tx.WithContext(ctx).
		Where("owner_type = ? AND owner_id = ? AND binding_key IN ?", ownerType, ownerID, currentBindingKeys).
		Delete(&model.PublicAssetBinding{})
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	if result.RowsAffected != int64(len(current)) {
		return errs.FailedPrecondition("public asset bindings changed concurrently; retry")
	}
	if err := transitionUnboundPublicAssetsToDeletePending(ctx, tx, assetIDs, now); err != nil {
		return errs.Internal(err)
	}
	return nil
}

func loadPublicAssetBinding(
	ctx context.Context,
	tx *gorm.DB,
	ownerType string,
	ownerID string,
	bindingKey string,
	lock bool,
) (model.PublicAssetBinding, bool, error) {
	var binding model.PublicAssetBinding
	query := tx.WithContext(ctx).
		Where("owner_type = ? AND owner_id = ? AND binding_key = ?", ownerType, ownerID, bindingKey)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	result := query.Limit(1).Find(&binding)
	if result.Error != nil {
		return model.PublicAssetBinding{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return model.PublicAssetBinding{}, false, nil
	}
	return binding, true, nil
}

// LoadPublicAssetBindingForUpdate reads one current binding while holding its
// row lock. A caller uses it when an enclosing domain mutation must decide
// whether its own semantic side effect is a no-op before replacing/releasing
// the binding.
func LoadPublicAssetBindingForUpdate(
	ctx context.Context,
	tx *gorm.DB,
	ownerType string,
	ownerID string,
	bindingKey string,
) (model.PublicAssetBinding, bool, error) {
	return loadPublicAssetBinding(ctx, tx, ownerType, ownerID, bindingKey, true)
}

func lockPublicAssetsForUpdate(
	ctx context.Context,
	tx *gorm.DB,
	assetIDs []string,
) (map[string]model.PublicAsset, error) {
	assetIDs = normalizedUniqueStrings(assetIDs)
	if len(assetIDs) == 0 {
		return map[string]model.PublicAsset{}, nil
	}
	var assets []model.PublicAsset
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "status").
		Where("id IN ?", assetIDs).
		Order("id ASC").
		Find(&assets).Error; err != nil {
		return nil, err
	}
	result := make(map[string]model.PublicAsset, len(assets))
	for _, asset := range assets {
		result[asset.ID] = asset
	}
	return result, nil
}

func lockPublicAssetBindingsForAssets(
	ctx context.Context,
	tx *gorm.DB,
	assetIDs []string,
) ([]model.PublicAssetBinding, error) {
	assetIDs = normalizedUniqueStrings(assetIDs)
	if len(assetIDs) == 0 {
		return nil, nil
	}
	var bindings []model.PublicAssetBinding
	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("asset_id").
		Where("asset_id IN ?", assetIDs).
		Order("asset_id ASC").
		Find(&bindings).Error
	return bindings, err
}

func transitionUnboundPublicAssetsToDeletePending(
	ctx context.Context,
	tx *gorm.DB,
	assetIDs []string,
	now time.Time,
) error {
	assetIDs = normalizedUniqueStrings(assetIDs)
	if len(assetIDs) == 0 {
		return nil
	}
	return tx.WithContext(ctx).Model(&model.PublicAsset{}).
		Where("id IN ? AND status = ?", assetIDs, model.PublicAssetStatusReady).
		Where("source_file_id IS NULL").
		Where("NOT EXISTS (SELECT 1 FROM public_asset_binding AS binding WHERE binding.asset_id = public_asset.id)").
		Updates(structured.Fields{
			"status":              model.PublicAssetStatusDeletePending,
			"delete_requested_at": now,
			"updated_at":          now,
		}).Error
}

func samePublicAssetBindings(left []model.PublicAssetBinding, right []model.PublicAssetBinding) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].OwnerType != right[index].OwnerType ||
			left[index].OwnerID != right[index].OwnerID ||
			left[index].BindingKey != right[index].BindingKey ||
			left[index].AssetID != right[index].AssetID {
			return false
		}
	}
	return true
}

func normalizedUniqueStrings(values []string) []string {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			unique[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (s *Lifecycle) CompleteMediaGeneration(
	ctx context.Context,
	result *commonv1.MediaGenerationWriteResult,
) (*model.MediaGeneration, error) {
	if result == nil {
		return nil, errs.Required("result")
	}
	generationID := strings.TrimSpace(result.GetGenerationId())
	if _, err := uuid.Parse(generationID); err != nil {
		return nil, errs.InvalidArgument("generation_id", "must be a UUID")
	}
	if len(result.GetManifestSha256()) != 32 || result.GetObjectCount() <= 0 || result.GetTotalSize() <= 0 {
		return nil, errs.InvalidArgument(
			"result",
			"manifest SHA-256, positive object_count, and positive total_size are required",
		)
	}
	now := s.now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var generation model.MediaGeneration
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", generationID).
			Take(&generation).Error; err != nil {
			return err
		}
		if generation.Status == model.MediaGenerationStatusReady {
			if generation.ObjectCount == nil || *generation.ObjectCount != result.GetObjectCount() ||
				generation.TotalSize == nil || *generation.TotalSize != result.GetTotalSize() ||
				!bytes.Equal(generation.ManifestSHA256, result.GetManifestSha256()) {
				return fmt.Errorf("media generation completion conflicts with ready metadata")
			}
			return nil
		}
		if generation.Status != model.MediaGenerationStatusAllocated {
			return fmt.Errorf("media generation is not allocated")
		}
		deleteAfter := now.Add(retiredMediaGenerationRetention)
		if err := tx.Model(&model.MediaGeneration{}).
			Where("file_id = ? AND status = ? AND id <> ?", generation.FileID, model.MediaGenerationStatusReady, generation.ID).
			Updates(structured.Fields{
				"status":       model.MediaGenerationStatusRetired,
				"retired_at":   now,
				"delete_after": deleteAfter,
				"updated_at":   now,
			}).Error; err != nil {
			return err
		}
		return tx.Model(&model.MediaGeneration{}).
			Where("id = ? AND status = ?", generation.ID, model.MediaGenerationStatusAllocated).
			Updates(structured.Fields{
				"manifest_sha256": append([]byte(nil), result.GetManifestSha256()...),
				"object_count":    result.GetObjectCount(),
				"total_size":      result.GetTotalSize(),
				"status":          model.MediaGenerationStatusReady,
				"ready_at":        now,
				"updated_at":      now,
			}).Error
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("media_generation", generationID)
		}
		return nil, errs.FailedPrecondition(err.Error())
	}
	var generation model.MediaGeneration
	if err := s.db.WithContext(ctx).Where("id = ?", generationID).Take(&generation).Error; err != nil {
		return nil, errs.Internal(err)
	}
	return &generation, nil
}

func (s *Lifecycle) loadPublicAsset(ctx context.Context, assetID string) (*model.PublicAsset, error) {
	var asset model.PublicAsset
	if err := s.db.WithContext(ctx).Where("id = ?", assetID).Take(&asset).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("public_asset", assetID)
		}
		return nil, errs.Internal(err)
	}
	return &asset, nil
}

func canonicalMimeType(value string) string {
	return strings.TrimSpace(strings.Split(value, ";")[0])
}

func normalizedOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func joinOriginPath(origin, resourcePath string) string {
	if origin == "" {
		return resourcePath
	}
	trimmedDomain := strings.TrimRight(origin, "/")
	if strings.HasPrefix(trimmedDomain, "http://") || strings.HasPrefix(trimmedDomain, "https://") {
		return trimmedDomain + resourcePath
	}
	return "https://" + trimmedDomain + resourcePath
}
