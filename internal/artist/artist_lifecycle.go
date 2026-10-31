package artist

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// CreateArtistCommand creates a draft Artist owned by the caller.
// AcceptLanguage helps pick the source locale when the caller's Member has
// no preference.
type CreateArtistCommand struct {
	Name           string
	Slug           *string
	RealName       *string
	CountryCode    *string
	Website        *string
	ParentArtistID *string
	SocialLinks    map[string]string
	Document       *contentv1.RichTextDocument
	AcceptLanguage string
}

// CreateArtist creates an Artist, its content document and source-locale
// title, and makes the caller its first durable Owner atomically.
func (s *Service) CreateArtist(ctx context.Context, command CreateArtistCommand) (*managev1.Artist, error) {
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, errAuthenticationRequired
	}
	name := strings.TrimSpace(command.Name)
	if name == "" {
		return nil, domainerr.Required("name")
	}
	if err := s.validateNewArtistSlug(ctx, command.Slug); err != nil {
		return nil, err
	}
	artist := newArtist(command)
	ownerMemberID := user.MemberID.String()
	var ownerIdentityID string
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		// A new Artist has no resource row to lock yet. The fresh check locks
		// the canonical principal before the first durable state change and
		// keeps that lock for the rest of the transaction.
		Access: mutation.FreshAdminCan(policyv1.Artist.Create()),
		Change: func(ctx context.Context, tx *gorm.DB) (string, error) {
			var err error
			ownerIdentityID, err = s.createArtistWithDB(ctx, tx, &artist, name, command, ownerMemberID)
			return artist.ID, err
		},
		Audit: func(id string) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditArtistCreated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewArtistCreatedAuditRecord(m, id)
			})
		},
		Relationships: func(id string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return artistEntity.CreateRelationships(id, ownerIdentityID, artist.ParentArtistID)
		},
	})
	if err != nil {
		return nil, err
	}
	document, err := s.overlayArtistSourceLocaleDocument(ctx, &artist)
	if err != nil {
		return nil, err
	}
	return s.artistWithReadyOg(ctx, &artist, document)
}

func (s *Service) validateNewArtistSlug(ctx context.Context, slug *string) error {
	if slug == nil || *slug == "" {
		return nil
	}
	if err := s.checkSlugAvailable(ctx, *slug, ""); err != nil {
		return err
	}
	return s.runtime.EnsureResourceRouteAvailable(ctx, s.db, "artist", "artists", *slug)
}

func newArtist(command CreateArtistCommand) model.Artist {
	artist := model.Artist{
		Status:   model.ArtistStatusDraft,
		RealName: command.RealName, CountryCode: command.CountryCode,
		Website: command.Website, SocialLinks: command.SocialLinks,
	}
	if command.Slug != nil && *command.Slug != "" {
		artist.Slug = command.Slug
	}
	if command.ParentArtistID != nil && *command.ParentArtistID != "" {
		artist.ParentArtistID = command.ParentArtistID
	}
	return artist
}

// createArtistWithDB stores artist with its content document, source-locale
// title and owner, requests its OG image, and returns the owner's Identity.
func (s *Service) createArtistWithDB(
	ctx context.Context,
	tx *gorm.DB,
	artist *model.Artist,
	name string,
	command CreateArtistCommand,
	ownerMemberID string,
) (string, error) {
	if err := requireArtistRouteAvailableWithDB(ctx, s.runtime, tx, artist.Slug); err != nil {
		return "", err
	}
	sourceLocale := s.translation.ResolveInitialSourceLocale(ctx, tx, command.AcceptLanguage)
	artist.SourceLocale = sourceLocale
	document, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
		Profile:      creativeContentProfile,
		SourceLocale: sourceLocale,
	})
	if err != nil {
		return "", normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	contentDocumentID := document.Document.ID.String()
	artist.ContentDocumentID = &contentDocumentID
	if err := tx.Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}, {Name: "created_at"}}}).
		Create(artist).Error; err != nil {
		return "", err
	}
	if artist.ParentArtistID != nil {
		if err := artistEntity.ValidateParentTransition(ctx, tx, artist.ID, *artist.ParentArtistID); err != nil {
			return "", err
		}
	}
	if _, err := initializeCreativeContentDocument(ctx, tx, s.contentBlocks, artist.ID, sourceLocale, document, command.Document); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	if err := saveArtistSourceLocaleDocumentState(ctx, tx, artist.ID, sourceLocale, translationLocaleDocumentSaveInput{
		SourceLocale: sourceLocale, Title: &name, Now: now,
	}); err != nil {
		return "", err
	}
	if err := touchArtistRootUpdatedAt(ctx, tx, artist.ID, now); err != nil {
		return "", err
	}
	if err := insertArtistOwner(tx, artist.ID, ownerMemberID, now); err != nil {
		return "", err
	}
	owner, err := authorizationtarget.RequireLocked(ctx, tx, ownerMemberID)
	if err != nil {
		return "", err
	}
	if _, err := s.runtime.RequestCurrentWithDB(ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_ARTIST, artist.ID, "", false, "artist_created"); err != nil {
		return "", err
	}
	return owner.IdentityID, nil
}

func requireArtistRouteAvailableWithDB(ctx context.Context, runtime Runtime, tx *gorm.DB, slug *string) error {
	if slug == nil || *slug == "" {
		return nil
	}
	return runtime.EnsureResourceRouteAvailableInTx(ctx, tx, "artist", "artists", *slug)
}

func insertArtistOwner(tx *gorm.DB, artistID, memberID string, now time.Time) error {
	return tx.Exec(`
		INSERT INTO artist_owner (artist_id, member_id, created_at)
		VALUES (?::uuid, ?::uuid, ?)
	`, artistID, memberID, now).Error
}

// DeleteArtistCommand deletes an Artist whose relations still match the
// preview revision the caller reviewed.
type DeleteArtistCommand struct {
	ID               string
	ExpectedRevision string
}

// artistDeletion is the SpiceDB snapshot an Artist deletion removes.
type artistDeletion struct {
	deleted  []policyv1.RelationshipMutation
	restored []policyv1.RelationshipMutation
}

// DeleteArtist deletes the Artist, its content document, share links,
// image bindings, OG image and SpiceDB relationships.
func (s *Service) DeleteArtist(ctx context.Context, command DeleteArtistCommand) error {
	if strings.TrimSpace(command.ExpectedRevision) == "" {
		return domainerr.Required("expected_revision")
	}
	snapshotPlan, err := policyv1.Artist.Snapshot(command.ID)
	if err != nil {
		return domainerr.InvalidField("id", "must identify an Artist")
	}
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[artistDeletion]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			if err := artistEntity.LockRoot(ctx, tx, command.ID); err != nil {
				return err
			}
			// Serialize the SpiceDB relationship snapshot with every Artist
			// reparent. The root lock is acquired first everywhere to avoid a
			// delete/reparent lock-order inversion for the same Artist.
			return artistEntity.LockParentGraph(ctx, tx)
		},
		Access: artistEntity.LockedAccess(s.spiceDB, command.ID, policyv1.Artist.Delete),
		Change: func(ctx context.Context, tx *gorm.DB) (artistDeletion, error) {
			return s.deleteArtistWithDB(ctx, tx, command, snapshotPlan)
		},
		Audit: func(artistDeletion) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditArtistDeleted, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewArtistDeletedAuditRecord(m, command.ID)
			})
		},
		Relationships: func(deletion artistDeletion) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return deletion.deleted, deletion.restored, nil
		},
	})
	return err
}

func (s *Service) deleteArtistWithDB(ctx context.Context, tx *gorm.DB, command DeleteArtistCommand, snapshotPlan policyv1.RelationshipSnapshotPlan) (artistDeletion, error) {
	impacts, err := s.loadArtistDeletionImpacts(ctx, tx, command.ID)
	if err != nil {
		return artistDeletion{}, err
	}
	if artistDeletionRevision(impacts) != command.ExpectedRevision {
		return artistDeletion{}, domainerr.Precondition("Artist relations changed; preview deletion again")
	}
	snapshots, _, err := s.spiceDB.SnapshotResourceRelationshipDescriptors(ctx, snapshotPlan)
	if err != nil {
		return artistDeletion{}, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	deleted, restored, err := artistEntity.DeletionRelationshipMutations(command.ID, snapshots)
	if err != nil {
		return artistDeletion{}, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if err := validateResourceDeletionAuthorizationBatchSize("artist", deleted, restored); err != nil {
		return artistDeletion{}, err
	}
	if err := s.shareLinks.DeleteArtistShareLinks(ctx, tx, command.ID); err != nil {
		return artistDeletion{}, err
	}
	if err := s.runtime.CancelAndReleaseEntityWithDB(ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_ARTIST, "artist", command.ID); err != nil {
		return artistDeletion{}, err
	}
	if err := s.runtime.ReleasePublicAssetBindings(ctx, tx, "artist", command.ID, "image"); err != nil {
		return artistDeletion{}, err
	}
	documentID, err := loadCreativeContentDocumentID(ctx, tx, command.ID)
	if err != nil {
		return artistDeletion{}, err
	}
	if err := s.contentBlocks.DeleteDocument(ctx, tx, documentID, creativeContentDocumentFence(artistContentEntity, command.ID)); err != nil {
		return artistDeletion{}, normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	result := tx.Delete(&model.Artist{}, "id = ?", command.ID)
	if result.Error != nil {
		return artistDeletion{}, result.Error
	}
	if result.RowsAffected == 0 {
		return artistDeletion{}, domainerr.ResourceNotFound("artist", command.ID)
	}
	return artistDeletion{deleted: deleted, restored: restored}, nil
}

// PublishArtistCommand publishes a draft Artist.
type PublishArtistCommand struct {
	ID string
}

// UnpublishArtistCommand returns a published Artist to draft.
type UnpublishArtistCommand struct {
	ID string
}

// PublishArtist publishes the Artist; a published Artist is left unchanged.
func (s *Service) PublishArtist(ctx context.Context, command PublishArtistCommand) (*managev1.ArtistLifecycleMutationResponse, error) {
	return s.transitionArtist(ctx, command.ID, artistTransition{
		target: model.ArtistStatusPublished, previous: sharedtelemetry.AuditStateDraft, next: sharedtelemetry.AuditStatePublished,
		fields: func(now time.Time) structured.Fields {
			return structured.Fields{"status": model.ArtistStatusPublished, "published_at": now, "updated_at": now}
		},
		apply: func(artist *model.Artist, now time.Time) {
			artist.Status = model.ArtistStatusPublished
			artist.PublishedAt = &now
			artist.UpdatedAt = &now
		},
		paths: []string{"state.status", "state.published_at"},
	})
}

// UnpublishArtist returns the Artist to draft; a draft Artist is left
// unchanged.
func (s *Service) UnpublishArtist(ctx context.Context, command UnpublishArtistCommand) (*managev1.ArtistLifecycleMutationResponse, error) {
	return s.transitionArtist(ctx, command.ID, artistTransition{
		target: model.ArtistStatusDraft, previous: sharedtelemetry.AuditStatePublished, next: sharedtelemetry.AuditStateDraft,
		fields: func(now time.Time) structured.Fields {
			return structured.Fields{"status": model.ArtistStatusDraft, "updated_at": now}
		},
		apply: func(artist *model.Artist, now time.Time) {
			artist.Status = model.ArtistStatusDraft
			artist.UpdatedAt = &now
		},
		paths: []string{"state.status"},
	})
}

// artistTransition moves an Artist to target; fields are the stored
// columns, apply mirrors them on the read row for the response, and paths
// are the changed fields its content.updated signal names.
type artistTransition struct {
	target         model.ArtistStatus
	previous, next sharedtelemetry.AuditState
	fields         func(time.Time) structured.Fields
	apply          func(*model.Artist, time.Time)
	paths          []string
}

// transitionArtist locks the Artist row before the fresh Publish check, so
// a role revoked while the transition waited on the row is observed.
func (s *Service) transitionArtist(ctx context.Context, id string, transition artistTransition) (*managev1.ArtistLifecycleMutationResponse, error) {
	var artist model.Artist
	now := dbtime.Now()
	changed, err := mutation.Run(ctx, s.mutations, mutation.Spec[bool]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return artistEntity.LockRoot(ctx, tx, id)
		},
		Access: artistEntity.LockedAccess(s.spiceDB, id, policyv1.Artist.Publish),
		Change: func(_ context.Context, tx *gorm.DB) (bool, error) {
			if err := tx.First(&artist, "id = ?", id).Error; err != nil {
				return false, err
			}
			if artist.Status == transition.target {
				return false, nil
			}
			return true, tx.Model(&artist).Updates(transition.fields(now)).Error
		},
		Audit: func(changed bool) mutation.Audit {
			if !changed {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditArtistUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewArtistLifecycleAuditRecord(m, artist.ID, transition.previous, transition.next)
			})
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, changed bool) error {
			if !changed {
				return nil
			}
			return contentsignal.PublishInTransaction(ctx, tx, s.publisher, buildManageStateTransitionContentUpdatedEvent(
				managev1.ContentEntityType_CONTENT_ENTITY_TYPE_ARTIST, artist.ID, transition.paths,
			))
		},
	})
	if err != nil {
		return nil, err
	}
	if changed {
		transition.apply(&artist, now)
	}
	return &managev1.ArtistLifecycleMutationResponse{
		Id: artist.ID, Changed: changed,
		Status:      artistStatusToProto(artist.Status),
		PublishedAt: timestampProtoPtr(artist.PublishedAt), UpdatedAt: timestampProtoPtr(artist.UpdatedAt),
	}, nil
}
