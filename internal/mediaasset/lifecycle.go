// Package mediaasset owns immutable public-asset and media-generation
// persistence, including the File row lock shared by attachment writers and
// deletion.
package mediaasset

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/mediamime"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
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
	Disposition      Disposition
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
	units     mutation.UnitOfWork[Store]
	cdnDomain string
	now       func() time.Time
}

// NewLifecycle creates a lifecycle over its units of work and CDN origin.
func NewLifecycle(units mutation.UnitOfWork[Store], cdnDomain string) *Lifecycle {
	if units == nil {
		panic("media asset lifecycle service: units are required")
	}
	return &Lifecycle{units: units, cdnDomain: cdnDomain, now: dbtime.Now}
}

func (s *Lifecycle) AllocatePublicAsset(
	ctx context.Context,
	input Allocation,
) (*Asset, *WriteTarget, error) {
	input.Kind = strings.TrimSpace(input.Kind)
	input.Extension = strings.ToLower(strings.TrimSpace(input.Extension))
	input.MimeType = canonicalMimeType(input.MimeType)
	if err := validatePublicAssetAllocation(input); err != nil {
		return nil, nil, err
	}

	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, input.Extension)
	if err != nil {
		return nil, nil, domainerr.InvalidField("extension", err.Error())
	}
	now := s.now().UTC()
	asset := Asset{
		ID:               assetID,
		SourceFileID:     normalizedOptionalString(input.SourceFileID),
		Kind:             input.Kind,
		ObjectKey:        objectKey,
		Extension:        input.Extension,
		MimeType:         input.MimeType,
		Disposition:      "inline",
		DownloadFilename: normalizedOptionalString(input.DownloadFilename),
		Status:           AssetAllocated,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	createAsset := func(unit mutation.Tx[Store]) error {
		tx := unit.Store()
		if asset.SourceFileID != nil {
			if err := tx.LockAttachableFiles(ctx, []string{*asset.SourceFileID}); err != nil {
				return err
			}
		}
		return tx.InsertAsset(ctx, asset)
	}
	if err := s.units.Transaction(ctx, false, createAsset); err != nil {
		return nil, nil, err
	}
	target := &WriteTarget{
		AssetID:          asset.ID,
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
		return domainerr.InvalidField("kind", "unsupported public asset kind")
	}
	if input.Extension == "" || input.MimeType == "" {
		return domainerr.InvalidField("asset", "extension and mime_type are required")
	}
	if expected := mediamime.GetExtensionFromMime(input.MimeType); expected == "bin" || expected != input.Extension {
		return domainerr.InvalidField("extension", "does not match the verified MIME type")
	}
	if reason := PublicAssetKindMediaContractViolation(input.Kind, input.Extension, input.MimeType); reason != "" {
		return domainerr.InvalidField("asset", reason)
	}
	if input.SourceFileID != nil {
		if _, err := uuid.Parse(strings.TrimSpace(*input.SourceFileID)); err != nil {
			return domainerr.InvalidField("source_file_id", "must be a UUID")
		}
	}
	switch input.Disposition {
	case DispositionInline:
		if normalizedOptionalString(input.DownloadFilename) != nil {
			return domainerr.InvalidField("download_filename", "is not supported for public assets")
		}
	case DispositionAttachment:
		return domainerr.InvalidField("disposition", "attachment public assets are not supported")
	default:
		return domainerr.InvalidField("disposition", "must be inline")
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
	result *WrittenAsset,
) (*Asset, error) {
	if result == nil {
		return nil, domainerr.Required("result")
	}
	assetID := strings.TrimSpace(result.AssetID)
	if _, err := uuid.Parse(assetID); err != nil {
		return nil, domainerr.InvalidField("asset_id", "must be a UUID")
	}
	if result.FileSize <= 0 {
		return nil, domainerr.InvalidField("file_size", "must be positive")
	}
	if len(result.SHA256) != 32 {
		return nil, domainerr.InvalidField("sha256", "must be a 32-byte SHA-256 digest")
	}

	now := s.now().UTC()
	err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		tx := unit.Store()
		asset, err := tx.LockAsset(ctx, assetID)
		if err != nil {
			return err
		}
		if asset.Status == AssetReady {
			if asset.FileSize == nil ||
				*asset.FileSize != result.FileSize ||
				!bytes.Equal(asset.SHA256, result.SHA256) {
				return fmt.Errorf("public asset completion conflicts with ready metadata")
			}
			return nil
		}
		if asset.Status != AssetAllocated {
			return fmt.Errorf("public asset is not allocated")
		}
		return tx.CompleteAsset(ctx, assetID, result.FileSize, result.SHA256, now)
	})
	if err != nil {
		if errors.Is(err, ErrAssetNotFound) {
			return nil, domainerr.ResourceNotFound("public_asset", assetID)
		}
		return nil, domainerr.Precondition(err.Error())
	}
	return s.loadPublicAsset(ctx, assetID)
}

func (s *Lifecycle) BindPublicAsset(ctx context.Context, input Binding) error {
	if err := normalizePublicAssetBindingInput(&input); err != nil {
		return err
	}
	return s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		tx := unit.Store()
		return s.bindPublicAsset(ctx, tx, input)
	})
}

func normalizePublicAssetBindingInput(input *Binding) error {
	input.AssetID = strings.TrimSpace(input.AssetID)
	input.OwnerType = strings.TrimSpace(input.OwnerType)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.BindingKey = strings.TrimSpace(input.BindingKey)
	if input.AssetID == "" || input.OwnerType == "" || input.OwnerID == "" || input.BindingKey == "" {
		return domainerr.InvalidField("binding", "asset_id, owner_type, owner_id, and binding_key are required")
	}
	return nil
}

func (s *Lifecycle) bindPublicAsset(
	ctx context.Context,
	tx Store,
	input Binding,
) error {
	existing, existingFound, err := tx.Binding(ctx, input.OwnerType, input.OwnerID, input.BindingKey)
	if err != nil {
		return err
	}
	assetIDs := []string{input.AssetID}
	if existingFound {
		assetIDs = append(assetIDs, existing.AssetID)
	}
	assets, err := tx.LockAssets(ctx, assetIDs)
	if err != nil {
		return err
	}
	asset, ok := assets[input.AssetID]
	if !ok || asset.Status != AssetReady {
		return domainerr.Precondition("only ready assets can be bound")
	}

	current, currentFound, err := tx.LockBinding(ctx, input.OwnerType, input.OwnerID, input.BindingKey)
	if err != nil {
		return err
	}
	if currentFound != existingFound || (currentFound && current.AssetID != existing.AssetID) {
		return domainerr.Precondition("public asset binding changed concurrently; retry")
	}
	return s.storePublicAssetBinding(ctx, tx, input, current, currentFound)
}

func (s *Lifecycle) storePublicAssetBinding(
	ctx context.Context,
	tx Store,
	input Binding,
	current AssetBinding,
	currentFound bool,
) error {
	now := s.now().UTC()
	if !currentFound {
		binding := AssetBinding{
			AssetID:      input.AssetID,
			OwnerType:    input.OwnerType,
			OwnerID:      input.OwnerID,
			BindingKey:   input.BindingKey,
			SourceFileID: normalizedOptionalString(input.SourceFileID),
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := tx.InsertBinding(ctx, binding); err != nil {
			if errors.Is(err, ErrBindingConflict) {
				return domainerr.Precondition("public asset binding changed concurrently; retry").WithCause(failurecause.WithAttrs(err,
					slog.String("asset_id", input.AssetID), slog.String("owner_type", input.OwnerType), slog.String("owner_id", input.OwnerID)))
			}
			return err
		}
		return nil
	}
	changed, err := tx.ReplaceBinding(ctx, AssetBinding{AssetID: input.AssetID, OwnerType: input.OwnerType, OwnerID: input.OwnerID, BindingKey: input.BindingKey, SourceFileID: normalizedOptionalString(input.SourceFileID), UpdatedAt: now}, current.AssetID)
	if err != nil {
		return err
	}
	if !changed {
		return domainerr.Precondition("public asset binding changed concurrently; retry")
	}
	if current.AssetID == input.AssetID {
		return nil
	}
	if err := tx.MarkUnboundAssetsDeletePending(ctx, []string{current.AssetID}, now); err != nil {
		return err
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
) (*Reference, error) {
	sourceFileID = strings.TrimSpace(sourceFileID)
	var asset *Reference
	err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		tx := unit.Store()
		if err := tx.LockAttachableFiles(ctx, []string{sourceFileID}); err != nil {
			return err
		}
		lifecycle := s
		var err error
		asset, err = lifecycle.readyAssetRefForSourceFile(ctx, tx, sourceFileID, expectedKind)
		if err != nil {
			return err
		}
		input := Binding{
			AssetID:      asset.AssetID,
			OwnerType:    ownerType,
			OwnerID:      ownerID,
			BindingKey:   bindingKey,
			SourceFileID: &sourceFileID,
		}
		if err := normalizePublicAssetBindingInput(&input); err != nil {
			return err
		}
		return lifecycle.bindPublicAsset(ctx, tx, input)
	})
	if err != nil {
		return nil, err
	}
	return asset, nil
}

func (s *Lifecycle) ReadyAssetRef(ctx context.Context, assetID string) (*Reference, error) {
	asset, err := s.loadPublicAsset(ctx, strings.TrimSpace(assetID))
	if err != nil {
		return nil, err
	}
	if asset.Status != AssetReady || asset.FileSize == nil || len(asset.SHA256) != 32 {
		return nil, domainerr.Precondition("public asset is not ready")
	}
	return s.AssetRef(*asset)
}

func (s *Lifecycle) ReadyAssetRefForSourceFile(ctx context.Context, sourceFileID, expectedKind string) (*Reference, error) {
	var ref *Reference
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		ref, err = s.readyAssetRefForSourceFile(ctx, store, sourceFileID, expectedKind)
		return err
	})
	return ref, err
}
func (s *Lifecycle) readyAssetRefForSourceFile(ctx context.Context, store Store, sourceFileID, expectedKind string) (*Reference, error) {
	expectedKind = strings.TrimSpace(expectedKind)
	if _, ok := publicAssetKinds[expectedKind]; !ok {
		return nil, domainerr.InvalidField("kind", "unsupported public asset kind")
	}
	asset, err := store.ReadyAssetForFile(ctx, sourceFileID, expectedKind)
	if errors.Is(err, ErrAssetNotFound) {
		return nil, domainerr.ResourceNotFound("public_asset_for_source_file", sourceFileID)
	}
	if err != nil {
		return nil, err
	}
	return s.AssetRef(asset)
}

func ReadyPublicAssetRefForSourceFile(
	ctx context.Context,
	units mutation.UnitOfWork[Store],
	cdnDomain string,
	sourceFileID string,
	expectedKind string,
) (*Reference, error) {
	return NewLifecycle(units, cdnDomain).ReadyAssetRefForSourceFile(ctx, sourceFileID, expectedKind)
}

// LoadReadyPublicAssetRefsForSourceFiles resolves the newest ready derivative
// for each source File in one query.
func LoadReadyPublicAssetRefsForSourceFiles(
	ctx context.Context,
	units mutation.UnitOfWork[Store],
	cdnDomain string,
	expectedKind string,
	sourceFileIDs []string,
) (map[string]*Reference, error) {
	expectedKind = strings.TrimSpace(expectedKind)
	if _, ok := publicAssetKinds[expectedKind]; !ok {
		return nil, domainerr.InvalidField("kind", "unsupported public asset kind")
	}
	sourceFileIDs = sliceutil.SortedUniqueTrimmed(sourceFileIDs)
	refs := make(map[string]*Reference, len(sourceFileIDs))
	if len(sourceFileIDs) == 0 {
		return refs, nil
	}
	var assets []Asset
	if err := units.Read(ctx, func(store Store) error {
		var err error
		assets, err = store.ReadyAssetsForFiles(ctx, expectedKind, sourceFileIDs)
		return err
	}); err != nil {
		return nil, err
	}
	lifecycle := NewLifecycle(units, cdnDomain)
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
func (s *Lifecycle) AssetRef(asset Asset) (*Reference, error) {
	if asset.Kind == "attachment" || asset.Disposition != "inline" {
		return nil, domainerr.Precondition("attachment public assets cannot be emitted")
	}
	assetPath, err := mediaauth.AssetPath(asset.ID, asset.Kind, asset.Extension)
	if err != nil {
		return nil, err
	}
	return &Reference{
		AssetID:     asset.ID,
		URL:         mediaauth.JoinOrigin(s.cdnDomain, assetPath),
		Extension:   asset.Extension,
		MimeType:    asset.MimeType,
		FileSize:    *asset.FileSize,
		SHA256:      append([]byte(nil), asset.SHA256...),
		Disposition: DispositionInline,
	}, nil
}

func (s *Lifecycle) RequestPublicAssetDeletion(ctx context.Context, assetID string) error {
	assetID = strings.TrimSpace(assetID)
	return s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		tx := unit.Store()
		asset, err := tx.LockAssetForDeletion(ctx, assetID)
		if errors.Is(err, ErrAssetNotFound) {
			return domainerr.ResourceNotFound("public_asset", assetID)
		}
		if err != nil {
			return err
		}
		switch asset.Status {
		case AssetDeletePending, AssetDeleted:
			return nil
		case AssetReady, AssetFailed:
		default:
			return domainerr.Precondition("public asset cannot be deleted from its current state")
		}
		bindings, err := tx.LockAssetBindings(ctx, []string{assetID})
		if err != nil {
			return err
		}
		if len(bindings) > 0 {
			return domainerr.Precondition("public asset still has bindings")
		}
		now := s.now().UTC()
		changed, err := tx.MarkAssetDeletePending(ctx, assetID, now)
		if err != nil {
			return err
		}
		if !changed {
			return domainerr.Precondition("public asset cannot be deleted from its current state")
		}
		return nil
	})
}

// ReleasePublicAssetBindings releases the binding family identified by
// bindingPrefix for one owner: the exact key `prefix` and every key of the
// form `prefix_*` (for example "og" releases "og" and "og_ko"). Binding keys
// use underscores only, so the family separator is "_". The prefix is matched
// literally rather than through LIKE, because "_" is a LIKE wildcard.
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
		return domainerr.InvalidField("binding", "owner type, owner ID, and binding prefix are required")
	}
	return s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		tx := unit.Store()
		bindingKeys, err := tx.BindingKeys(ctx, ownerType, ownerID, bindingPrefix)
		if err != nil {
			return err
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
	bindingKeys = sliceutil.SortedUniqueTrimmed(bindingKeys)
	if ownerType == "" || ownerID == "" {
		return domainerr.InvalidField("binding", "owner type and owner ID are required")
	}
	if len(bindingKeys) == 0 {
		return nil
	}
	return s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		tx := unit.Store()
		return releaseExactPublicAssetBindings(ctx, tx, ownerType, ownerID, bindingKeys, s.now().UTC())
	})
}

func releaseExactPublicAssetBindings(
	ctx context.Context,
	tx Store,
	ownerType string,
	ownerID string,
	bindingKeys []string,
	now time.Time,
) error {
	snapshot, err := tx.Bindings(ctx, ownerType, ownerID, bindingKeys)
	if err != nil {
		return err
	}
	if len(snapshot) == 0 {
		return nil
	}

	assetIDs := make([]string, 0, len(snapshot))
	for _, binding := range snapshot {
		assetIDs = append(assetIDs, binding.AssetID)
	}
	if _, err := tx.LockAssets(ctx, assetIDs); err != nil {
		return err
	}

	current, err := tx.LockBindings(ctx, ownerType, ownerID, bindingKeys)
	if err != nil {
		return err
	}
	if !samePublicAssetBindings(snapshot, current) {
		return domainerr.Precondition("public asset bindings changed concurrently; retry")
	}

	currentBindingKeys := make([]string, 0, len(current))
	for _, binding := range current {
		currentBindingKeys = append(currentBindingKeys, binding.BindingKey)
	}
	removed, err := tx.DeleteBindings(ctx, ownerType, ownerID, currentBindingKeys)
	if err != nil {
		return err
	}
	if removed != int64(len(current)) {
		return domainerr.Precondition("public asset bindings changed concurrently; retry")
	}
	return tx.MarkUnboundAssetsDeletePending(ctx, assetIDs, now)
}

func samePublicAssetBindings(left []AssetBinding, right []AssetBinding) bool {
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

func (s *Lifecycle) CompleteMediaGeneration(
	ctx context.Context,
	result *WrittenGeneration,
) (*Generation, error) {
	if result == nil {
		return nil, domainerr.Required("result")
	}
	generationID := strings.TrimSpace(result.GenerationID)
	if _, err := uuid.Parse(generationID); err != nil {
		return nil, domainerr.InvalidField("generation_id", "must be a UUID")
	}
	if len(result.ManifestSHA256) != 32 || result.ObjectCount <= 0 || result.TotalSize <= 0 {
		return nil, domainerr.InvalidField(
			"result",
			"manifest SHA-256, positive object_count, and positive total_size are required",
		)
	}
	now := s.now().UTC()
	err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		tx := unit.Store()
		generation, err := tx.LockGeneration(ctx, generationID)
		if err != nil {
			return err
		}
		if generation.Status == GenerationReady {
			if generation.ObjectCount == nil || *generation.ObjectCount != result.ObjectCount ||
				generation.TotalSize == nil || *generation.TotalSize != result.TotalSize ||
				!bytes.Equal(generation.ManifestSHA256, result.ManifestSHA256) {
				return fmt.Errorf("media generation completion conflicts with ready metadata")
			}
			return nil
		}
		if generation.Status != GenerationAllocated {
			return fmt.Errorf("media generation is not allocated")
		}
		deleteAfter := now.Add(retiredMediaGenerationRetention)
		if err := tx.RetireOtherGenerations(ctx, generation.FileID, generation.ID, now, deleteAfter); err != nil {
			return err
		}
		return tx.CompleteGeneration(ctx, generation.ID, result.ManifestSHA256, result.ObjectCount, result.TotalSize, now)
	})
	if err != nil {
		if errors.Is(err, ErrGenerationNotFound) {
			return nil, domainerr.ResourceNotFound("media_generation", generationID)
		}
		return nil, domainerr.Precondition(err.Error())
	}
	var generation Generation
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		generation, err = store.Generation(ctx, generationID)
		return err
	}); err != nil {
		return nil, err
	}
	return &generation, nil
}

func (s *Lifecycle) loadPublicAsset(ctx context.Context, assetID string) (*Asset, error) {
	var asset Asset
	err := s.units.Read(ctx, func(store Store) error { var err error; asset, err = store.Asset(ctx, assetID); return err })
	if errors.Is(err, ErrAssetNotFound) {
		return nil, domainerr.ResourceNotFound("public_asset", assetID)
	}
	if err != nil {
		return nil, err
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
