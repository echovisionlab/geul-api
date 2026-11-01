package release

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// SetReleaseArtworkCommand makes an existing File the Release artwork.
type SetReleaseArtworkCommand struct {
	ReleaseID string
	FileID    string
}

// DeleteReleaseArtworkCommand removes the Release artwork relation without
// deleting the File.
type DeleteReleaseArtworkCommand struct {
	ReleaseID string
}

// artworkChange is the File an artwork mutation attached or removed; FileID
// is empty when nothing changed.
type artworkChange struct {
	FileID string
	Asset  *commonv1.AssetRef
}

// SetReleaseArtwork sets the release artwork and returns its ready asset, or
// nil when the File already was the artwork.
func (s *Service) SetReleaseArtwork(ctx context.Context, command SetReleaseArtworkCommand) (*commonv1.AssetRef, error) {
	if err := s.requireArtworkRelease(ctx, command.ReleaseID); err != nil {
		return nil, err
	}
	var storedFileID string
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		storedFileID, err = store.FileID(ctx, command.FileID)
		return err
	}); err != nil {
		return nil, missingAs(err, ErrFileNotFound, domainerr.ResourceNotFound("file", command.FileID))
	}
	change, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[artworkChange, Store]{
		Lock:   lockReleaseRow(command.ReleaseID, nil),
		Access: s.releaseAccess(command.ReleaseID, releaseActionEdit),
		Change: func(ctx context.Context, store Store) (artworkChange, error) {
			return replaceReleaseArtwork(ctx, store, command.ReleaseID, command.FileID, storedFileID)
		},
		Audit: func(change artworkChange) mutation.Audit {
			return releaseArtworkAudit(command.ReleaseID, change.FileID, sharedtelemetry.AuditCollectionOperationAdded)
		},
		Outbox: s.publishArtworkChange(command.ReleaseID),
	})
	return change.Asset, err
}

// DeleteReleaseArtwork removes the release artwork; an absent artwork is
// not an error.
func (s *Service) DeleteReleaseArtwork(ctx context.Context, command DeleteReleaseArtworkCommand) error {
	if err := s.requireArtworkRelease(ctx, command.ReleaseID); err != nil {
		return err
	}
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[artworkChange, Store]{
		Lock:   lockReleaseRow(command.ReleaseID, nil),
		Access: s.releaseAccess(command.ReleaseID, releaseActionEdit),
		Change: func(ctx context.Context, store Store) (artworkChange, error) {
			return removeReleaseArtwork(ctx, store, command.ReleaseID)
		},
		Audit: func(change artworkChange) mutation.Audit {
			return releaseArtworkAudit(command.ReleaseID, change.FileID, sharedtelemetry.AuditCollectionOperationRemoved)
		},
		Outbox: s.publishArtworkChange(command.ReleaseID),
	})
	return err
}

// requireArtworkRelease answers not found for a missing Release before the
// File is looked up, and fails like a read when its content document cannot
// be projected.
func (s *Service) requireArtworkRelease(ctx context.Context, releaseID string) error {
	if err := s.units.Read(ctx, func(store Store) error {
		return store.ReleaseExists(ctx, releaseID)
	}); err != nil {
		return releaseMissingAs(err, releaseNotFound(releaseID))
	}
	_, err := s.overlayReleaseSourceLocaleDocument(ctx, Release{ID: releaseID})
	return err
}

// replaceReleaseArtwork attaches the requested File; storedFileID is the same
// File as the File row reads it.
func replaceReleaseArtwork(ctx context.Context, store Store, releaseID, fileID, storedFileID string) (artworkChange, error) {
	if err := store.LockAttachableFiles(ctx, []string{fileID}); err != nil {
		return artworkChange{}, err
	}
	existingFileID, exists, err := store.ArtworkFile(ctx, releaseID)
	if err != nil {
		return artworkChange{}, err
	}
	if exists && existingFileID == fileID {
		return artworkChange{}, nil
	}
	if err := store.SetArtworkFile(ctx, releaseID, fileID); err != nil {
		return artworkChange{}, err
	}
	if err := touchRelease(ctx, store, releaseID); err != nil {
		return artworkChange{}, err
	}
	asset, err := store.BindArtwork(ctx, storedFileID, releaseID)
	if err != nil {
		return artworkChange{}, err
	}
	if err := store.CancelOG(ctx, releaseID); err != nil {
		return artworkChange{}, err
	}
	return artworkChange{FileID: fileID, Asset: asset}, nil
}

func removeReleaseArtwork(ctx context.Context, store Store, releaseID string) (artworkChange, error) {
	existingFileID, exists, err := store.ArtworkFile(ctx, releaseID)
	if err != nil {
		return artworkChange{}, err
	}
	if !exists {
		return artworkChange{}, nil
	}
	if err := store.ClearArtworkFiles(ctx, releaseID); err != nil {
		return artworkChange{}, err
	}
	if err := touchRelease(ctx, store, releaseID); err != nil {
		return artworkChange{}, err
	}
	if err := store.ReleaseArtworkBinding(ctx, releaseID); err != nil {
		return artworkChange{}, err
	}
	if err := store.CancelOG(ctx, releaseID); err != nil {
		return artworkChange{}, err
	}
	return artworkChange{FileID: existingFileID}, nil
}

// touchRelease marks the locked Release row changed by a relation mutation.
func touchRelease(ctx context.Context, store Store, releaseID string) error {
	return store.TouchRelease(ctx, releaseID, time.Now())
}

func releaseArtworkAudit(releaseID, fileID string, op sharedtelemetry.AuditCollectionOperation) mutation.Audit {
	if fileID == "" {
		return mutation.Unchanged()
	}
	return mutation.RequestAudit(sharedtelemetry.AuditReleaseUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewReleaseArtworkAuditRecord(m, releaseID, fileID, op)
	})
}

func (s *Service) publishArtworkChange(releaseID string) func(context.Context, Store, artworkChange) error {
	return func(ctx context.Context, store Store, change artworkChange) error {
		if change.FileID == "" {
			return nil
		}
		return publishContentUpdated(ctx, store, buildReleaseMediaMutationContentUpdatedEvent(releaseID, "media.artwork"))
	}
}
