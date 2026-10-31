package release

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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
	Type            model.ReleaseType
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
	Status      model.ReleaseStatus
	PublishedAt *time.Time
	UpdatedAt   time.Time
}

// CreateRelease creates a draft Release (Site Admin only).
func (s *Service) CreateRelease(ctx context.Context, command CreateReleaseCommand) (*managev1.Release, error) {
	title := strings.TrimSpace(command.Title)
	if title == "" {
		return nil, domainerr.Required("title")
	}
	requestedSourceLocale := command.SourceLocale
	if requestedSourceLocale != "" {
		var err error
		requestedSourceLocale, err = normalizeReleaseDocumentLocale(requestedSourceLocale)
		if err != nil {
			return nil, domainerr.InvalidField("source_locale", "must be a supported canonical locale")
		}
	}
	release := model.Release{
		Type: command.Type, Status: model.ReleaseStatusDraft,
		ReleaseDate: command.ReleaseDate, SpotifyURL: command.SpotifyURL, AppleMusicURL: command.AppleMusicURL,
		BandcampURL: command.BandcampURL, YoutubeMusicURL: command.YoutubeMusicURL, CatalogNumber: command.CatalogNumber,
	}
	if command.Slug != nil {
		if err := pathslug.Validate(*command.Slug); err != nil {
			return nil, err
		}
		if err := routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "release", "releases", *command.Slug); err != nil {
			return nil, err
		}
		release.Slug = command.Slug
	}
	created, err := mutation.Run(ctx, s.mutations, mutation.Spec[model.Release]{
		// A new Release has no row to lock yet; its route is the only state
		// another creation can race for.
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			if release.Slug == nil || *release.Slug == "" {
				return nil
			}
			return routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "release", "releases", *release.Slug)
		},
		Access: mutation.FreshAdminCan(policyv1.Release.Create()),
		Change: func(ctx context.Context, tx *gorm.DB) (model.Release, error) {
			sourceLocale := requestedSourceLocale
			if sourceLocale == "" {
				sourceLocale = resolveInitialSourceLocale(ctx, tx, command.AcceptLanguage)
			}
			if err := s.createReleaseRows(ctx, tx, &release, sourceLocale, title, command.Document); err != nil {
				return model.Release{}, err
			}
			return release, nil
		},
		Audit: func(release model.Release) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditReleaseCreated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewReleaseCreatedAuditRecord(m, release.ID)
			})
		},
		Relationships: func(release model.Release) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			touchPolicy, err := policyv1.Release.TouchPolicy(release.ID)
			if err != nil {
				return nil, nil, err
			}
			deletePolicy, err := policyv1.Release.DeletePolicy(release.ID)
			return []policyv1.RelationshipMutation{touchPolicy}, []policyv1.RelationshipMutation{deletePolicy}, err
		},
	})
	if err != nil {
		if dberrors.IsUniqueViolation(err) {
			return nil, slugTaken(err)
		}
		return nil, err
	}
	document, err := s.overlayReleaseSourceLocaleDocument(ctx, &created)
	if err != nil {
		return nil, err
	}
	return toProtoRelease(&created, document, nil), nil
}

// createReleaseRows stores the Release root, its content document and its
// source locale title.
func (s *Service) createReleaseRows(
	ctx context.Context,
	tx *gorm.DB,
	release *model.Release,
	sourceLocale string,
	title string,
	initial *contentv1.RichTextDocument,
) error {
	release.SourceLocale = sourceLocale
	document, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
		Profile:      releaseContentProfile,
		SourceLocale: sourceLocale,
	})
	if err != nil {
		return normalizeReleaseContentBlockError(err)
	}
	contentDocumentID := document.Document.ID.String()
	release.ContentDocumentID = &contentDocumentID
	if err := tx.
		Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}, {Name: "created_at"}, {Name: "updated_at"}}}).
		Create(release).Error; err != nil {
		return err
	}
	if _, err := initializeReleaseContentDocument(
		ctx, tx, s.contentBlocks, release.ID, sourceLocale, document, initial,
		func(context.Context, *gorm.DB) error { return nil },
	); err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := saveReleaseSourceLocaleMetadata(ctx, tx, release.ID, sourceLocale, translationLocaleDocumentSaveInput{
		Title: &title, Now: now,
	}); err != nil {
		return err
	}
	return touchReleaseSourceLocaleState(ctx, tx, release.ID, now)
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
		if err := s.waveformJobs.CancelTracks(
			ctx,
			trackIDs,
			managev1.TranscodeCancelReason_TRANSCODE_CANCEL_REASON_USER_DELETED,
		); err != nil {
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
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		Lock:   lockReleaseRow(releaseID, nil),
		Access: s.releaseAccess(releaseID, action),
		Change: func(context.Context, *gorm.DB) (struct{}, error) { return struct{}{}, nil },
		Audit:  func(struct{}) mutation.Audit { return mutation.Unchanged() },
	})
	return err
}

func (s *Service) cleanupReleaseTrackUploadSessions(ctx context.Context, releaseID string) error {
	for range MaxTrackUploadCleanupPasses {
		var trackIDs []string
		var sessionsRemaining bool
		if err := mutation.ReadCurrent(ctx, s.db, func(tx *gorm.DB) error {
			var err error
			trackIDs, err = lockReleaseTrackIDs(ctx, tx, releaseID)
			if err != nil {
				return err
			}
			sessionsRemaining, err = hasTrackUploadSessions(ctx, tx, trackIDs)
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
func lockReleaseTrackIDs(ctx context.Context, tx *gorm.DB, releaseID string) ([]string, error) {
	if _, err := lockReleaseForUpdate(ctx, tx, releaseID); err != nil {
		return nil, err
	}
	var trackIDs []string
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Model(&model.Track{}).
		Where("release_id = ?", releaseID).
		Pluck("id", &trackIDs).Error; err != nil {
		return nil, err
	}
	if err := validateReleaseCascadeAuthorizationBatchSize(len(trackIDs)); err != nil {
		return nil, err
	}
	return trackIDs, nil
}

func hasTrackUploadSessions(ctx context.Context, tx *gorm.DB, trackIDs []string) (bool, error) {
	if len(trackIDs) == 0 {
		return false, nil
	}
	var count int64
	if err := tx.WithContext(ctx).Model(&model.UploadSession{}).
		Where("upload_type = ? AND entity_id IN ?", managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(), trackIDs).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}

func classifyReleaseDeleteError(releaseID string, err error) error {
	switch {
	case errors.Is(err, ErrTrackUploadSessionNotAbortable):
		return domainerr.Precondition("track audio upload is finalizing; retry its completion before deleting the release").WithCause(err)
	case errors.Is(err, gorm.ErrRecordNotFound):
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
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			var err error
			trackIDs, err = lockReleaseTrackIDs(ctx, tx, releaseID)
			return err
		},
		Access: s.releaseAccess(releaseID, releaseActionDelete),
		Change: func(ctx context.Context, tx *gorm.DB) (struct{}, error) {
			sessionsRemaining, err := hasTrackUploadSessions(ctx, tx, trackIDs)
			if err != nil {
				return struct{}{}, err
			}
			if sessionsRemaining {
				return struct{}{}, ErrTrackUploadSessionsChanged
			}
			return struct{}{}, s.deleteReleaseCascade(ctx, tx, releaseID)
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
func (s *Service) deleteReleaseCascade(ctx context.Context, tx *gorm.DB, releaseID string) error {
	if err := tx.
		Where("entity_type = ? AND entity_id = ?", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_RELEASE.String(), releaseID).
		Delete(&model.ShareLink{}).Error; err != nil {
		return err
	}
	if err := s.og.CancelAndRelease(ctx, tx, releaseID); err != nil {
		return err
	}
	if err := s.assets.ReleaseArtwork(ctx, tx, releaseID); err != nil {
		return err
	}
	documentID, err := loadReleaseContentDocumentID(ctx, tx, releaseID)
	if err != nil {
		return err
	}
	if err := s.contentBlocks.DeleteDocument(
		ctx,
		tx,
		documentID,
		releaseContentDocumentFence(releaseID, func(context.Context, *gorm.DB) error { return nil }),
	); err != nil {
		return normalizeReleaseContentBlockError(err)
	}
	result := tx.Delete(&model.Release{}, "id = ?", releaseID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return releaseNotFound(releaseID)
	}
	return nil
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
	return s.transitionRelease(ctx, command.ID, model.ReleaseStatusPublished, sharedtelemetry.AuditStatePublished)
}

// UnpublishRelease returns a release to draft (admin only).
func (s *Service) UnpublishRelease(ctx context.Context, command UnpublishReleaseCommand) (LifecycleChange, error) {
	return s.transitionRelease(ctx, command.ID, model.ReleaseStatusDraft, sharedtelemetry.AuditStateDraft)
}

// transitionRelease moves the Release to next. A Release already in next
// reports no change.
func (s *Service) transitionRelease(
	ctx context.Context,
	releaseID string,
	next model.ReleaseStatus,
	nextAudit sharedtelemetry.AuditState,
) (LifecycleChange, error) {
	var locked model.Release
	now := dbtime.Now()
	return mutation.Run(ctx, s.mutations, mutation.Spec[LifecycleChange]{
		Lock:   lockReleaseRow(releaseID, &locked),
		Access: s.releaseAccess(releaseID, releaseActionPublish),
		Change: func(_ context.Context, tx *gorm.DB) (LifecycleChange, error) {
			change := LifecycleChange{
				ID: locked.ID, Status: model.ReleaseStatus(locked.Status),
				PublishedAt: locked.PublishedAt, UpdatedAt: locked.UpdatedAt,
			}
			if change.Status == next {
				return change, nil
			}
			columns := structured.Fields{"status": string(next), "updated_at": now}
			if next == model.ReleaseStatusPublished {
				columns["published_at"] = now
				change.PublishedAt = &now
			}
			if err := tx.Model(&model.Release{}).Where("id = ?", locked.ID).Updates(columns).Error; err != nil {
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
		Outbox: func(ctx context.Context, tx *gorm.DB, change LifecycleChange) error {
			if !change.Changed {
				return nil
			}
			paths := []string{"state.status"}
			if next == model.ReleaseStatusPublished {
				paths = append(paths, "state.published_at")
			}
			return s.publishContentUpdated(ctx, tx, buildReleaseStateTransitionContentUpdatedEvent(change.ID, paths))
		},
	})
}
