package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediaauth"
	telemetry "github.com/echovisionlab/geul-telemetry"
)

var ErrFileStillReferenced = errors.New("file still has active product references")

type FileDeletionMutation struct {
	FileID               string
	RelatedOutputFileIDs []string
	AlreadyPending       bool
}
type DeletionMeshCandidate struct {
	ID, Status   string
	OutputFileID *string
}
type FileGenerationDeleteRef struct{ ID, FileID, Kind, ObjectPrefix string }

const MeshCandidateRetention = 7 * 24 * time.Hour

func MeshOutputProtectionStatuses() []string { return []string{"pending", "processing", "ready"} }

type DeletionLifecycleStore interface {
	LockDeletionCandidate(context.Context, string) (File, error)
	LockDeletionCandidates(context.Context, []string) ([]File, error)
	ActiveFileReferenceNames(context.Context, string) ([]string, error)
	ActiveFileReferenceIDs(context.Context, []string) (map[string]struct{}, error)
	LockDerivativeDeletionAssets(context.Context, string) ([]mediaasset.Asset, error)
	LockSourceDeletionAssets(context.Context, string) ([]mediaasset.Asset, error)
	BoundDeletionAssetIDs(context.Context, []string) (map[string]bool, error)
	RequestDeletionAsset(context.Context, string) error
	RequestDeletionFavicon(context.Context, string) error
	LockDeletionMeshCandidates(context.Context, string) ([]DeletionMeshCandidate, error)
	CancelDeletionMeshCandidate(context.Context, string, time.Time, time.Time) error
	DeleteDeletionMeshCandidate(context.Context, string) error
	FileCount(context.Context, []string) (int64, error)
	MarkFileDeletion(context.Context, string, time.Time) (bool, error)
	DeletionEventFile(context.Context, string) (File, error)
	DeletionGenerationRefs(context.Context, string) ([]FileGenerationDeleteRef, error)
	EnqueueFileDeletion(context.Context, DeletionTarget) error
	DeletionAudit(domainaudit.Appender) domainaudit.Appender
	LockFolderSubtree(context.Context, string) ([]string, error)
	FolderDeletionFileIDs(context.Context, []string) ([]string, error)
	DetachDeletionFiles(context.Context, []string) error
	DeleteFolder(context.Context, string) error
}

func MarkFileDeletionRequested(ctx context.Context, store DeletionLifecycleStore, id string, now time.Time) (*FileDeletionMutation, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	file, err := store.LockDeletionCandidate(ctx, id)
	if errors.Is(err, ErrFileNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if file.DeleteRequestedAt != nil {
		return &FileDeletionMutation{FileID: id, AlreadyPending: true}, nil
	}
	refs, err := store.ActiveFileReferenceNames(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(refs) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrFileStillReferenced, strings.Join(refs, ", "))
	}
	return MarkLockedFileDeletionRequested(ctx, store, file, now)
}
func MarkLockedFileDeletionRequested(ctx context.Context, store DeletionLifecycleStore, file File, now time.Time) (*FileDeletionMutation, error) {
	id := file.ID
	if err := store.RequestDeletionFavicon(ctx, id); err != nil {
		return nil, err
	}
	if err := RequestUnboundDerivativeAssetsDeletion(ctx, store, id); err != nil {
		return nil, err
	}
	if err := RequestUnboundSourceAssetsDeletion(ctx, store, id); err != nil {
		return nil, err
	}
	outputs, err := PrepareMeshCandidatesForDeletion(ctx, store, id, now)
	if err != nil {
		return nil, err
	}
	changed, err := store.MarkFileDeletion(ctx, id, now)
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, fmt.Errorf("file %s deletion intent changed while locked", id)
	}
	return &FileDeletionMutation{FileID: id, RelatedOutputFileIDs: outputs}, nil
}
func RequestUnboundDerivativeAssetsDeletion(ctx context.Context, store DeletionLifecycleStore, id string) error {
	assets, err := store.LockDerivativeDeletionAssets(ctx, id)
	if err != nil {
		return err
	}
	return requestUnboundAssetsDeletion(ctx, store, assets, "file derivative public asset has an unsupported lifecycle state")
}
func RequestUnboundSourceAssetsDeletion(ctx context.Context, store DeletionLifecycleStore, id string) error {
	assets, err := store.LockSourceDeletionAssets(ctx, id)
	if err != nil {
		return err
	}
	return requestUnboundAssetsDeletion(ctx, store, assets, "source File public asset has an unsupported lifecycle state")
}
func requestUnboundAssetsDeletion(ctx context.Context, store DeletionLifecycleStore, assets []mediaasset.Asset, unsupported string) error {
	if len(assets) == 0 {
		return nil
	}
	ids := make([]string, 0, len(assets))
	for _, a := range assets {
		ids = append(ids, a.ID)
	}
	bound, err := store.BoundDeletionAssetIDs(ctx, ids)
	if err != nil {
		return err
	}
	for _, a := range assets {
		if bound[a.ID] {
			continue
		}
		switch a.Status {
		case mediaasset.AssetReady, mediaasset.AssetFailed:
			if err := store.RequestDeletionAsset(ctx, a.ID); err != nil {
				return err
			}
		case mediaasset.AssetDeletePending, mediaasset.AssetDeleted, mediaasset.AssetAllocated:
			continue
		default:
			return domainerr.Precondition(unsupported)
		}
	}
	return nil
}
func PrepareMeshCandidatesForDeletion(ctx context.Context, store DeletionLifecycleStore, id string, now time.Time) ([]string, error) {
	candidates, err := store.LockDeletionMeshCandidates(ctx, id)
	if err != nil {
		return nil, err
	}
	outputs := make([]string, 0, len(candidates))
	for _, c := range candidates {
		switch c.Status {
		case "pending", "processing", "cancelled":
			if err := store.CancelDeletionMeshCandidate(ctx, c.ID, now, now.Add(MeshCandidateRetention)); err != nil {
				return nil, err
			}
		default:
			if c.OutputFileID != nil && strings.TrimSpace(*c.OutputFileID) != "" {
				output := strings.TrimSpace(*c.OutputFileID)
				n, err := store.FileCount(ctx, []string{output})
				if err != nil {
					return nil, err
				}
				if n > 0 {
					outputs = append(outputs, output)
				}
			}
			if err := store.DeleteDeletionMeshCandidate(ctx, c.ID); err != nil {
				return nil, err
			}
		}
	}
	return outputs, nil
}
func PublishFileDeletionMutation(ctx context.Context, store DeletionLifecycleStore, deletion *FileDeletionMutation, now time.Time) error {
	for _, id := range deletion.RelatedOutputFileIDs {
		if id == deletion.FileID {
			continue
		}
		other, err := MarkFileDeletionRequested(ctx, store, id, now)
		if err != nil {
			return err
		}
		if other != nil {
			if err := PublishFileDeletionMutation(ctx, store, other, now); err != nil {
				return err
			}
		}
	}
	target, err := BuildFileDeletionTarget(ctx, store, deletion.FileID)
	if err != nil {
		return err
	}
	return store.EnqueueFileDeletion(ctx, *target)
}
func BuildFileDeletionTarget(ctx context.Context, store DeletionLifecycleStore, id string) (*DeletionTarget, error) {
	file, err := store.DeletionEventFile(ctx, id)
	if err != nil {
		return nil, err
	}
	original, err := CanonicalObjectTarget(file)
	if err != nil {
		return nil, err
	}
	refs, err := store.DeletionGenerationRefs(ctx, id)
	if err != nil {
		return nil, err
	}
	generations, err := ClassifyGenerationDeleteRefs(id, refs)
	if err != nil {
		return nil, err
	}
	return &DeletionTarget{FileID: id, Original: original, Generations: generations, Timestamp: time.Now().UTC()}, nil
}
func ClassifyGenerationDeleteRefs(id string, refs []FileGenerationDeleteRef) ([]*GenerationTarget, error) {
	result := make([]*GenerationTarget, 0, len(refs))
	for _, r := range refs {
		prefix, err := mediaauth.MediaHLSObjectPrefix(id, r.ID)
		if err != nil || r.FileID != id || r.Kind != "hls" || r.ObjectPrefix != prefix {
			return nil, fmt.Errorf("media generation does not match file deletion authority")
		}
		result = append(result, &GenerationTarget{GenerationID: r.ID, FileID: id, ObjectPrefix: r.ObjectPrefix})
	}
	return result, nil
}
func AppendDeletedAudit(ctx context.Context, writer domainaudit.Appender, id string) error {
	return domainaudit.AppendOptionalRequest(ctx, nil, writer, telemetry.AuditFileDeleted, func(m telemetry.AuditMetadata) (telemetry.AuditRecord, error) {
		return telemetry.NewFileDeletedAuditRecord(m, id)
	})
}
