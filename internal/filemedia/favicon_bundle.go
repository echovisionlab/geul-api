package filemedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	stderrors "errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/mutation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

const (
	faviconBundleTimeout      = 20 * time.Second
	faviconCleanupItemTimeout = 3 * time.Second
)

var errFaviconBundleCommitUncertain = stderrors.New("favicon bundle commit outcome is uncertain")

func (s *Service) generateFaviconBundle(
	ctx context.Context,
	sourceFileID string,
) (*commonv1.AssetRef, error) {
	bundleCtx, cancel := context.WithTimeout(ctx, s.faviconGenerationTimeout())
	defer cancel()
	sourceFileID = strings.TrimSpace(sourceFileID)

	existing, err := s.loadFaviconSet(bundleCtx, sourceFileID)
	if err != nil {
		return nil, domainerr.Precondition(fmt.Sprintf("existing favicon bundle is invalid: %v", err))
	}
	if existing != nil {
		return mediaasset.ReferenceProto(existing.IconPNG32), nil
	}

	source, sourceBytes, err := s.loadValidatedFaviconSource(bundleCtx, sourceFileID)
	if err != nil {
		return nil, err
	}
	outputs, err := s.generateFaviconOutputs(bundleCtx, sourceBytes, source.MimeType)
	if err != nil {
		return nil, err
	}
	staged, err := s.stageFaviconBundle(ctx, bundleCtx, sourceBytes, source.MimeType, outputs)
	if err != nil {
		return nil, err
	}
	return s.commitFaviconBundle(ctx, bundleCtx, source, staged)
}

func (s *Service) faviconGenerationTimeout() time.Duration {
	if s.testFaviconBundleTimeout > 0 {
		return s.testFaviconBundleTimeout
	}
	return faviconBundleTimeout
}

func (s *Service) loadValidatedFaviconSource(
	ctx context.Context,
	sourceFileID string,
) (File, []byte, error) {
	var source File
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		source, err = store.FaviconSource(ctx, sourceFileID, false)
		return err
	}); err != nil {
		return File{}, nil, fmt.Errorf("load favicon source: %w", err)
	}
	objectKey, err := mediaauth.MediaObjectKey(source.ID, source.Extension)
	if err != nil {
		return File{}, nil, domainerr.Precondition("favicon source metadata is not canonical").WithCause(failurecause.ServerFault(failurecause.WithAttrs(err, slog.String("file_id", source.ID))))
	}
	object, err := s.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.s3Bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return File{}, nil, fmt.Errorf("read favicon source: %w", err)
	}
	sourceBytes, readErr := io.ReadAll(io.LimitReader(object.Body, favicon.MaxSourceSize+1))
	closeErr := object.Body.Close()
	if readErr != nil {
		return File{}, nil, fmt.Errorf("read favicon source: %w", readErr)
	}
	if closeErr != nil {
		return File{}, nil, fmt.Errorf("close favicon source: %w", closeErr)
	}
	if int64(len(sourceBytes)) != source.FileSize {
		return File{}, nil, domainerr.Precondition("favicon source size does not match File")
	}
	if err := favicon.ValidateSource(sourceBytes, source.MimeType); err != nil {
		return File{}, nil, domainerr.Precondition(fmt.Sprintf("favicon source validation failed: %v", err))
	}
	return source, sourceBytes, nil
}

func (s *Service) generateFaviconOutputs(
	ctx context.Context,
	sourceBytes []byte,
	sourceMime string,
) ([]favicon.Output, error) {
	processor := s.faviconProcessor
	if processor == nil {
		processor = favicon.NewProcessor()
	}
	// Process an isolated copy so the exact bytes validated above remain the
	// only bytes eligible for source-backed SVG publication.
	outputs, err := processor.Process(ctx, append([]byte(nil), sourceBytes...), sourceMime)
	if err != nil {
		return nil, domainerr.Precondition(fmt.Sprintf("favicon processing failed: %v", err))
	}
	if err := favicon.ValidateOutputs(outputs, sourceMime); err != nil {
		// The processor generated the bundle; an invalid one is a server fault.
		return nil, domainerr.Precondition(fmt.Sprintf("favicon processing produced an invalid bundle: %v", err)).WithCause(failurecause.ServerFault(err))
	}
	return outputs, nil
}

func (s *Service) stageFaviconBundle(
	cleanupCtx context.Context,
	ctx context.Context,
	sourceBytes []byte,
	sourceMime string,
	outputs []favicon.Output,
) ([]stagedFaviconAsset, error) {
	staged := make([]stagedFaviconAsset, 0, len(outputs)+1)
	if canonicalMimeType(sourceMime) == "image/svg+xml" {
		asset, stageErr := s.stageFaviconAsset(ctx, sourceBytes, mediaasset.Allocation{
			Kind:        "favicon",
			Extension:   "svg",
			MimeType:    "image/svg+xml",
			Disposition: mediaasset.DispositionInline,
		}, favicon.DerivativeType(""), true)
		if asset.AssetID != "" {
			staged = append(staged, asset)
		}
		if stageErr != nil {
			s.scheduleStagedFaviconDeletion(cleanupCtx, staged, nil)
			return nil, fmt.Errorf("stage favicon SVG source asset: %w", stageErr)
		}
	}
	for _, output := range outputs {
		asset, stageErr := s.stageFaviconAsset(ctx, output.Data, mediaasset.Allocation{
			Kind:        "favicon",
			Extension:   output.Spec.Extension,
			MimeType:    output.Spec.MimeType,
			Disposition: mediaasset.DispositionInline,
		}, output.Spec.DerivativeType, false)
		if asset.AssetID != "" {
			staged = append(staged, asset)
		}
		if stageErr != nil {
			s.scheduleStagedFaviconDeletion(cleanupCtx, staged, nil)
			return nil, fmt.Errorf(
				"stage favicon derivative %s: %w",
				output.Spec.DerivativeType.String(),
				stageErr,
			)
		}
	}
	return staged, nil
}

func (s *Service) commitFaviconBundle(
	cleanupCtx context.Context,
	ctx context.Context,
	source File,
	staged []stagedFaviconAsset,
) (*commonv1.AssetRef, error) {
	var set *favicon.Set
	var usedStaged bool
	result := s.faviconCommits.Commit(ctx, func(store Store) error {
		var err error
		set, usedStaged, err = s.finalizeStagedFaviconBundle(ctx, store, source, staged)
		return err
	})
	switch result.Stage {
	case domain.FaviconBeginFailed:
		s.scheduleStagedFaviconDeletion(cleanupCtx, staged, nil)
		return nil, fmt.Errorf("begin favicon bundle transaction: %w", result.Err)
	case domain.FaviconChangeFailed:
		s.scheduleStagedFaviconDeletion(cleanupCtx, staged, nil)
		return nil, fmt.Errorf("store generated favicon bundle: %w", result.Err)
	}
	commitErr := result.Err
	if commitErr == nil && s.testFaviconCommitError != nil {
		commitErr = s.testFaviconCommitError
	}
	if commitErr != nil {
		slog.ErrorContext(ctx, "Favicon bundle commit failed; reconciling", failurecause.Attr(commitErr), "file_id", source.ID)
		winner, reconcileErr := s.reconcileFaviconBundle(cleanupCtx, source.ID)
		if reconcileErr == nil && winner != nil {
			s.scheduleStagedFaviconDeletion(cleanupCtx, staged, favicon.AssetIDs(winner))
			return mediaasset.ReferenceProto(winner.IconPNG32), nil
		}
		slog.ErrorContext(ctx, "favicon bundle commit outcome could not be reconciled; preserving source and staged assets", failurecause.Attr(stderrors.Join(commitErr, reconcileErr)), "file_id", source.ID)
		return nil, fmt.Errorf(
			"%w: commit error: %v; reconcile error: %v",
			errFaviconBundleCommitUncertain,
			commitErr,
			reconcileErr,
		)
	}
	if !usedStaged {
		s.scheduleStagedFaviconDeletion(cleanupCtx, staged, favicon.AssetIDs(set))
	}
	return mediaasset.ReferenceProto(set.IconPNG32), nil
}

type stagedFaviconAsset struct {
	AssetID        string
	DerivativeType favicon.DerivativeType
	SourceSVG      bool
}

func (s *Service) stageFaviconAsset(
	ctx context.Context,
	data []byte,
	allocation mediaasset.Allocation,
	derivativeType favicon.DerivativeType,
	sourceSVG bool,
) (stagedFaviconAsset, error) {
	var target *mediaasset.WriteTarget
	err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		var err error
		_, target, err = unit.Store().AllocatePromotionAsset(ctx, s.cdnDomain, allocation)
		return err
	})
	if err != nil {
		return stagedFaviconAsset{}, err
	}
	staged := stagedFaviconAsset{
		AssetID:        target.AssetID,
		DerivativeType: derivativeType,
		SourceSVG:      sourceSVG,
	}
	if _, err := s.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.s3Bucket),
		Key:           aws.String(target.ObjectKey),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
		ContentType:   aws.String(allocation.MimeType),
	}); err != nil {
		return staged, err
	}
	digest := sha256.Sum256(data)
	if err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		return unit.Store().CompletePromotionAsset(ctx, s.cdnDomain, mediaasset.WrittenAsset{AssetID: target.AssetID, FileSize: int64(len(data)), SHA256: digest[:]})
	}); err != nil {
		return staged, err
	}
	return staged, nil
}

func (s *Service) finalizeStagedFaviconBundle(
	ctx context.Context,
	store Store,
	source File,
	staged []stagedFaviconAsset,
) (*favicon.Set, bool, error) {
	if _, err := store.FaviconSource(ctx, source.ID, true); err != nil {
		return nil, false, err
	}
	existing, err := store.FaviconSet(ctx, s.cdnDomain, source.ID)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		return existing, false, nil
	}

	stagedByType := make(map[favicon.DerivativeType]stagedFaviconAsset, len(staged))
	for _, asset := range staged {
		if asset.SourceSVG {
			attached, err := store.AttachFaviconSource(ctx, asset.AssetID, source.ID, time.Now().UTC())
			if err != nil {
				return nil, false, err
			}
			if !attached {
				return nil, false, fmt.Errorf("staged SVG favicon asset is not attachable")
			}
			continue
		}
		stagedByType[asset.DerivativeType] = asset
	}
	for _, spec := range favicon.RequiredOutputs() {
		asset, ok := stagedByType[spec.DerivativeType]
		if !ok {
			return nil, false, fmt.Errorf("staged favicon bundle is missing %s", spec.DerivativeType.String())
		}
		if err := store.InsertFaviconDerivative(ctx, uuid.NewString(), source.ID, spec.DerivativeType.String(), asset.AssetID); err != nil {
			return nil, false, err
		}
	}
	set, err := store.FaviconSet(ctx, s.cdnDomain, source.ID)
	if err != nil {
		return nil, false, err
	}
	if set == nil || set.IconPNG32 == nil {
		return nil, false, fmt.Errorf("generated favicon set is incomplete")
	}
	return set, true, nil
}

func (s *Service) reconcileFaviconBundle(
	ctx context.Context,
	sourceFileID string,
) (*favicon.Set, error) {
	if s.testFaviconReconcileError != nil {
		return nil, s.testFaviconReconcileError
	}
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), faviconCleanupItemTimeout)
	defer cancel()
	return s.loadFaviconSet(reconcileCtx, sourceFileID)
}

func (s *Service) scheduleStagedFaviconDeletion(
	ctx context.Context,
	staged []stagedFaviconAsset,
	preserve map[string]struct{},
) {
	ids := make([]string, 0, len(staged))
	for _, asset := range staged {
		if _, keep := preserve[asset.AssetID]; !keep && asset.AssetID != "" {
			ids = append(ids, asset.AssetID)
		}
	}
	if len(ids) == 0 {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), faviconCleanupItemTimeout)
	defer cancel()
	now := time.Now().UTC()
	err := s.units.Transaction(cleanupCtx, false, func(unit mutation.Tx[Store]) error {
		return unit.Store().ScheduleStagedFaviconDeletion(cleanupCtx, ids, now)
	})
	if err != nil {
		slog.ErrorContext(ctx, "failed to schedule staged favicon assets for durable cleanup", "asset_ids", ids, failurecause.Attr(err))
	}
}

func (s *Service) loadFaviconSet(ctx context.Context, id string) (*favicon.Set, error) {
	var set *favicon.Set
	err := s.readUploadFacts(ctx, func(store Store) error {
		var err error
		set, err = store.FaviconSet(ctx, s.cdnDomain, id)
		return err
	})
	return set, err
}
