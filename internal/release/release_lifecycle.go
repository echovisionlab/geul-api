package release

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// A Release cascade writes one policy deletion for the Release itself, leaving
// the rest of the shared SpiceDB atomic relationship batch for Track policies.
//
//lint:ignore U1000 Tests use the named domain limit to verify the atomic authorization boundary.
const maxReleaseCascadeAuthorizationTracks = maxSpiceDBAtomicRelationshipMutations - 1

func validateReleaseCascadeAuthorizationBatchSize(trackCount int) error {
	return validateAtomicAuthorizationRelationshipBatchSize(
		trackCount+1,
		trackCount+1,
		"release has too many tracks to delete atomically; delete tracks individually until at most 999 remain",
	)
}

// CreateReleaseCommand creates a draft Release with its source locale title
// and optional initial document.
type CreateReleaseCommand struct {
	Title string
	// SourceLocale is the explicit source locale; empty resolves it from the
	// Member preference, AcceptLanguage and the translation default.
	SourceLocale    string
	AcceptLanguage  string
	Type            Type
	Slug            *string
	ReleaseDate     *time.Time
	SpotifyURL      *string
	AppleMusicURL   *string
	BandcampURL     *string
	YoutubeMusicURL *string
	CatalogNumber   *string
	Document        *contentv1.RichTextDocument
}

// DeleteReleaseCommand deletes a Release and every Track it owns.
type DeleteReleaseCommand struct {
	ID string
}

// PublishReleaseCommand publishes a draft Release.
type PublishReleaseCommand struct {
	ID string
}

// UnpublishReleaseCommand returns a published Release to draft.
type UnpublishReleaseCommand struct {
	ID string
}

// LifecycleChange is the Release state after a publish or unpublish.
type LifecycleChange struct {
	ID          string
	Changed     bool
	Status      Status
	PublishedAt *time.Time
	UpdatedAt   time.Time
}

// CreateRelease creates a draft Release (Site Admin only).
func (s *Service) CreateRelease(ctx context.Context, command CreateReleaseCommand) (View, error) {
	title := strings.TrimSpace(command.Title)
	if title == "" {
		return View{}, domainerr.Required("title")
	}
	requestedSourceLocale := command.SourceLocale
	if requestedSourceLocale != "" {
		var err error
		requestedSourceLocale, err = normalizeReleaseDocumentLocale(requestedSourceLocale)
		if err != nil {
			return View{}, domainerr.InvalidField("source_locale", "must be a supported canonical locale")
		}
	}
	release := Release{
		Type: command.Type, Status: StatusDraft,
		ReleaseDate: command.ReleaseDate, SpotifyURL: command.SpotifyURL, AppleMusicURL: command.AppleMusicURL,
		BandcampURL: command.BandcampURL, YoutubeMusicURL: command.YoutubeMusicURL, CatalogNumber: command.CatalogNumber,
	}
	if command.Slug != nil {
		if err := pathslug.Validate(*command.Slug); err != nil {
			return View{}, err
		}
		if err := s.checkResourceRoute(ctx, *command.Slug); err != nil {
			return View{}, err
		}
		release.Slug = command.Slug
	}
	created, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[Release, Store]{
		// A new Release has no row to lock yet; its route is the only state
		// another creation can race for.
		Lock: func(ctx context.Context, store Store) error {
			if release.Slug == nil || *release.Slug == "" {
				return nil
			}
			return store.LockResourceRoute(ctx, *release.Slug)
		},
		Access: mutation.FreshAdminCan(policyv1.Release.Create()),
		Change: func(ctx context.Context, store Store) (Release, error) {
			sourceLocale := requestedSourceLocale
			if sourceLocale == "" {
				sourceLocale = resolveInitialSourceLocale(ctx, store, command.AcceptLanguage)
			}
			if err := createReleaseRows(ctx, store, &release, sourceLocale, title, command.Document); err != nil {
				return Release{}, err
			}
			return release, nil
		},
		Audit: func(release Release) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditReleaseCreated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewReleaseCreatedAuditRecord(m, release.ID)
			})
		},
		Relationships: func(release Release) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			touchPolicy, err := policyv1.Release.TouchPolicy(release.ID)
			if err != nil {
				return nil, nil, err
			}
			deletePolicy, err := policyv1.Release.DeletePolicy(release.ID)
			return []policyv1.RelationshipMutation{touchPolicy}, []policyv1.RelationshipMutation{deletePolicy}, err
		},
	})
	if err != nil {
		if errors.Is(err, ErrSlugTaken) {
			return View{}, slugTaken(err)
		}
		return View{}, err
	}
	return s.overlayReleaseSourceLocaleDocument(ctx, created)
}

// checkResourceRoute requires, in its own read snapshot, that no route uses
// the Release slug.
func (s *Service) checkResourceRoute(ctx context.Context, slug string) error {
	return s.units.Read(ctx, func(store Store) error {
		return store.CheckResourceRoute(ctx, slug)
	})
}

// createReleaseRows stores the Release root, its content document and its
// source locale title.
func createReleaseRows(
	ctx context.Context,
	store Store,
	release *Release,
	sourceLocale string,
	title string,
	initial *contentv1.RichTextDocument,
) error {
	release.SourceLocale = sourceLocale
	document, err := store.CreateContentDocument(ctx, contentblock.CreateInput{
		Profile:      releaseContentProfile,
		SourceLocale: sourceLocale,
	})
	if err != nil {
		return normalizeReleaseContentBlockError(err)
	}
	contentDocumentID := document.Document.ID.String()
	release.ContentDocumentID = &contentDocumentID
	if err := store.InsertRelease(ctx, release); err != nil {
		return err
	}
	if _, err := initializeReleaseContentDocument(
		ctx, store, release.ID, sourceLocale, document, initial,
	); err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := saveReleaseSourceLocaleMetadata(ctx, store, release.ID, sourceLocale, translationLocaleDocumentSaveInput{
		Title: &title, Now: now,
	}); err != nil {
		return err
	}
	return touchReleaseSourceLocaleState(ctx, store, release.ID, now)
}

// DeleteRelease deletes a Release and every cascade-owned Track (Site Admin only).
func (s *Service) DeleteRelease(ctx context.Context, command DeleteReleaseCommand) error {
	releaseID := command.ID
	// Upload cleanup aborts uploads outside any transaction, so the caller's
	// authority is decided under the Release lock before it runs.
	if err := s.authorizeLockedRelease(ctx, releaseID, releaseActionDelete); err != nil {
		return err
	}
	// Upload cleanup runs outside the deletion transaction, so an upload can
	// start in between; the deletion then fails and cleanup runs again.
	var uploadsChanged error
	for range MaxTrackUploadCleanupPasses {
		if err := s.cleanupReleaseTrackUploadSessions(ctx, releaseID); err != nil {
			return classifyReleaseDeleteError(releaseID, fmt.Errorf("cleanup release track audio uploads: %w", err))
		}
		trackIDs, err := s.deleteReleaseOnce(ctx, releaseID)
		if errors.Is(err, ErrTrackUploadSessionsChanged) {
			uploadsChanged = err
			continue
		}
		if err != nil {
			return classifyReleaseDeleteError(releaseID, err)
		}
		if err := s.waveformJobs.CancelDeletedTracks(ctx, trackIDs); err != nil {
			slog.ErrorContext(ctx, "Failed to cancel waveform jobs for deleted release", "releaseId", releaseID, failurecause.Attr(err))
		}
		return nil
	}
	return domainerr.Precondition("track audio uploads changed during deletion; retry the request").WithCause(uploadsChanged)
}

// authorizeLockedRelease decides the caller's authority for action while the
// Release row is locked, for a use case whose next step has effects outside
// the database and so cannot wait for the mutation's own check.
func (s *Service) authorizeLockedRelease(ctx context.Context, releaseID string, action releaseObjectAction) error {
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Lock:   lockReleaseRow(releaseID, nil),
		Access: s.releaseAccess(releaseID, action),
		Change: func(context.Context, Store) (struct{}, error) { return struct{}{}, nil },
		Audit:  func(struct{}) mutation.Audit { return mutation.Unchanged() },
	})
	return err
}

func (s *Service) cleanupReleaseTrackUploadSessions(ctx context.Context, releaseID string) error {
	for range MaxTrackUploadCleanupPasses {
		var trackIDs []string
		var sessionsRemaining bool
		if err := s.units.ReadCurrent(ctx, func(store Store) error {
			var err error
			trackIDs, err = lockReleaseTrackIDs(ctx, store, releaseID)
			if err != nil {
				return err
			}
			sessionsRemaining, err = store.HasTrackUploadSessions(ctx, trackIDs)
			return err
		}); err != nil {
			return err
		}
		if !sessionsRemaining {
			return nil
		}
		for _, trackID := range trackIDs {
			if err := s.trackFiles.CleanupTrackUploadSessions(ctx, trackID, "Release deleted"); err != nil {
				return err
			}
		}
	}
	return ErrTrackUploadSessionsChanged
}

// lockReleaseTrackIDs locks the Release and its Tracks and returns the Track
// IDs, which must fit one authorization cascade batch.
func lockReleaseTrackIDs(ctx context.Context, store Store, releaseID string) ([]string, error) {
	if _, err := store.LockRelease(ctx, releaseID, UpdateLock); err != nil {
		return nil, err
	}
	trackIDs, err := store.LockTrackIDs(ctx, releaseID)
	if err != nil {
		return nil, err
	}
	if err := validateReleaseCascadeAuthorizationBatchSize(len(trackIDs)); err != nil {
		return nil, err
	}
	return trackIDs, nil
}

func classifyReleaseDeleteError(releaseID string, err error) error {
	switch {
	case errors.Is(err, ErrTrackUploadSessionNotAbortable):
		return domainerr.Precondition("track audio upload is finalizing; retry its completion before deleting the release").WithCause(err)
	case errors.Is(err, ErrReleaseNotFound):
		return releaseNotFound(releaseID)
	default:
		return err
	}
}

// deleteReleaseOnce deletes the Release, its owned rows and the Release and
// Track authorization policies in one transaction, and returns the deleted
// Track IDs. It fails with ErrTrackUploadSessionsChanged when an upload
// started after the cleanup pass, so the caller cleans up again.
func (s *Service) deleteReleaseOnce(ctx context.Context, releaseID string) ([]string, error) {
	var trackIDs []string
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			trackIDs, err = lockReleaseTrackIDs(ctx, store, releaseID)
			return err
		},
		Access: s.releaseAccess(releaseID, releaseActionDelete),
		Change: func(ctx context.Context, store Store) (struct{}, error) {
			sessionsRemaining, err := store.HasTrackUploadSessions(ctx, trackIDs)
			if err != nil {
				return struct{}{}, err
			}
			if sessionsRemaining {
				return struct{}{}, ErrTrackUploadSessionsChanged
			}
			return struct{}{}, deleteReleaseCascade(ctx, store, releaseID)
		},
		Audit: func(struct{}) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditReleaseDeleted, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewReleaseDeletedAuditRecord(m, releaseID)
			})
		},
		Relationships: func(struct{}) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return releaseAuthzMutations(releaseID, trackIDs)
		},
	})
	return trackIDs, err
}

// deleteReleaseCascade removes the Release row and what it owns outside the
// database cascade: share links, OG and artwork assets and its content
// document.
func deleteReleaseCascade(ctx context.Context, store Store, releaseID string) error {
	if err := store.DeleteShareLinks(ctx, releaseID); err != nil {
		return err
	}
	if err := store.CancelOG(ctx, releaseID); err != nil {
		return err
	}
	if err := store.ReleaseArtworkBinding(ctx, releaseID); err != nil {
		return err
	}
	documentID, err := loadReleaseContentDocumentID(ctx, store, releaseID, Unlocked)
	if err != nil {
		return err
	}
	if err := store.DeleteContentDocument(ctx, documentID, releaseContentDocumentFence(releaseID)); err != nil {
		return normalizeReleaseContentBlockError(err)
	}
	return releaseMissingAs(store.DeleteRelease(ctx, releaseID), releaseNotFound(releaseID))
}

// releaseAuthzMutations deletes the Release and Track policies and, as
// compensation, restores them.
func releaseAuthzMutations(releaseID string, trackIDs []string) (apply, compensate []policyv1.RelationshipMutation, err error) {
	removeReleasePolicy, err := policyv1.Release.DeletePolicy(releaseID)
	if err != nil {
		return nil, nil, err
	}
	restoreReleasePolicy, err := policyv1.Release.TouchPolicy(releaseID)
	if err != nil {
		return nil, nil, err
	}
	apply = make([]policyv1.RelationshipMutation, 0, len(trackIDs)+1)
	compensate = make([]policyv1.RelationshipMutation, 0, len(trackIDs)+1)
	apply = append(apply, removeReleasePolicy)
	compensate = append(compensate, restoreReleasePolicy)
	for _, trackID := range trackIDs {
		removePolicy, err := policyv1.Track.DeletePolicy(trackID)
		if err != nil {
			return nil, nil, err
		}
		restorePolicy, err := policyv1.Track.TouchPolicy(trackID)
		if err != nil {
			return nil, nil, err
		}
		apply = append(apply, removePolicy)
		compensate = append(compensate, restorePolicy)
	}
	return apply, compensate, nil
}

// PublishRelease publishes a release (admin only).
func (s *Service) PublishRelease(ctx context.Context, command PublishReleaseCommand) (LifecycleChange, error) {
	return s.transitionRelease(ctx, command.ID, StatusPublished, sharedtelemetry.AuditStatePublished)
}

// UnpublishRelease returns a release to draft (admin only).
func (s *Service) UnpublishRelease(ctx context.Context, command UnpublishReleaseCommand) (LifecycleChange, error) {
	return s.transitionRelease(ctx, command.ID, StatusDraft, sharedtelemetry.AuditStateDraft)
}

// transitionRelease moves the Release to next. A Release already in next
// reports no change.
func (s *Service) transitionRelease(
	ctx context.Context,
	releaseID string,
	next Status,
	nextAudit sharedtelemetry.AuditState,
) (LifecycleChange, error) {
	var locked Release
	now := dbtime.Now()
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[LifecycleChange, Store]{
		Lock:   lockReleaseRow(releaseID, &locked),
		Access: s.releaseAccess(releaseID, releaseActionPublish),
		Change: func(ctx context.Context, store Store) (LifecycleChange, error) {
			change := LifecycleChange{
				ID: locked.ID, Status: locked.Status,
				PublishedAt: locked.PublishedAt, UpdatedAt: locked.UpdatedAt,
			}
			if change.Status == next {
				return change, nil
			}
			var publishedAt *time.Time
			if next == StatusPublished {
				publishedAt = &now
				change.PublishedAt = &now
			}
			if err := store.SetReleaseStatus(ctx, locked.ID, next, publishedAt, now); err != nil {
				return LifecycleChange{}, err
			}
			change.Changed, change.Status, change.UpdatedAt = true, next, now
			return change, nil
		},
		Audit: func(change LifecycleChange) mutation.Audit {
			if !change.Changed {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditReleaseUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewReleaseLifecycleAuditRecord(m, change.ID, releaseAuditState(locked.Status), nextAudit)
			})
		},
		Outbox: func(ctx context.Context, store Store, change LifecycleChange) error {
			if !change.Changed {
				return nil
			}
			paths := []string{"state.status"}
			if next == StatusPublished {
				paths = append(paths, "state.published_at")
			}
			return publishContentUpdated(ctx, store, buildReleaseStateTransitionContentUpdatedEvent(change.ID, paths))
		},
	})
}
