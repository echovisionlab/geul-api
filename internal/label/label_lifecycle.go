package label

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const creativeContentProfile = "compact"

// CreateLabelCommand creates a draft Label owned by the caller. Optional
// strings left nil are not set; a blank one is stored as NULL.
// AcceptLanguage helps pick the source locale when the caller's Member has
// no preference.
type CreateLabelCommand struct {
	Name           string
	Slug           *string
	CountryCode    *string
	Website        *string
	ParentLabelID  *string
	SocialLinks    map[string]string
	Document       *contentv1.RichTextDocument
	AcceptLanguage string
}

// CreateLabel creates a Label, its content document and its source-locale
// title, and makes the caller its first Owner.
func (s *Service) CreateLabel(ctx context.Context, command CreateLabelCommand) (View, error) {
	user := auth.GetUser(ctx)
	if user == nil {
		return View{}, errAuthenticationRequired
	}
	name := strings.TrimSpace(command.Name)
	if name == "" {
		return View{}, domainerr.Required("name")
	}
	if err := s.validateNewLabelSlug(ctx, command.Slug); err != nil {
		return View{}, err
	}
	label := newLabel(command)
	ownerMemberID := user.MemberID.String()
	var ownerIdentityID string
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		// A new Label has no resource row to lock yet. The fresh check locks
		// the canonical principal before the first durable state change and
		// keeps that lock for the rest of the transaction.
		Access: mutation.FreshAdminCan(policyv1.Label.Create()),
		Change: func(ctx context.Context, store Store) (string, error) {
			var err error
			ownerIdentityID, err = createLabel(ctx, store, &label, name, command.Document, ownerMemberID, command.AcceptLanguage)
			return label.ID, err
		},
		Audit: func(id string) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditLabelCreated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewLabelCreatedAuditRecord(m, id)
			})
		},
		Relationships: func(id string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return labelEntity.CreateRelationships(id, ownerIdentityID, label.ParentLabelID)
		},
	})
	if err != nil {
		return View{}, err
	}
	document, err := s.overlayLabelSourceLocaleDocument(ctx, &label)
	if err != nil {
		return View{}, err
	}
	return s.labelWithReadyOg(ctx, label, document, nil)
}

func (s *Service) validateNewLabelSlug(ctx context.Context, slug *string) error {
	if slug == nil || *slug == "" {
		return nil
	}
	return s.units.Read(ctx, func(store Store) error {
		if err := checkSlugAvailable(ctx, store, *slug, ""); err != nil {
			return err
		}
		return store.CheckResourceRoute(ctx, *slug)
	})
}

func newLabel(command CreateLabelCommand) Label {
	label := Label{
		Status: StatusDraft,
		Slug:   command.Slug, SocialLinks: command.SocialLinks,
	}
	if countryCode, present := normalizeOptionalNullableString(command.CountryCode); present {
		label.CountryCode = countryCode
	}
	if website, present := normalizeOptionalNullableString(command.Website); present {
		label.Website = website
	}
	if parentLabelID, present := normalizeOptionalNullableString(command.ParentLabelID); present {
		label.ParentLabelID = parentLabelID
	}
	return label
}

func normalizeOptionalNullableString(value *string) (*string, bool) {
	if value == nil {
		return nil, false
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, true
	}
	return &trimmed, true
}

// createLabel stores label with its content document, owner and
// source-locale title, and returns the owner's Identity.
func createLabel(
	ctx context.Context,
	store Store,
	label *Label,
	name string,
	documentInput *contentv1.RichTextDocument,
	ownerMemberID string,
	acceptLanguage string,
) (string, error) {
	if label.Slug != nil && *label.Slug != "" {
		if err := store.LockResourceRoute(ctx, *label.Slug); err != nil {
			return "", err
		}
	}
	sourceLocale := store.ResolveInitialSourceLocale(ctx, acceptLanguage)
	label.SourceLocale = sourceLocale
	document, err := store.CreateContentDocument(ctx, contentblock.CreateInput{
		Profile:      creativeContentProfile,
		SourceLocale: sourceLocale,
	})
	if err != nil {
		return "", normalizeLabelContentBlockError(err)
	}
	contentDocumentID := document.Document.ID.String()
	label.ContentDocumentID = &contentDocumentID
	if err := store.InsertLabel(ctx, label); err != nil {
		if errors.Is(err, ErrSlugTaken) {
			return "", domainerr.New(domainerr.AlreadyExists, "", "label with this slug already exists").WithCause(err)
		}
		return "", err
	}
	if label.ParentLabelID != nil {
		if err := labelEntity.ValidateParentTransition(ctx, store, label.ID, *label.ParentLabelID); err != nil {
			return "", err
		}
	}
	if err := store.InsertCreatorOwner(ctx, label.ID, ownerMemberID); err != nil {
		return "", err
	}
	ownerIdentityID, err := store.LockMemberIdentity(ctx, ownerMemberID)
	if err != nil {
		return "", err
	}
	if _, err := initializeCreativeContentDocument(ctx, store, label.ID, sourceLocale, document, documentInput); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	if err := saveLabelSourceTitle(ctx, store, label.ID, sourceLocale, &name, now); err != nil {
		return "", err
	}
	return ownerIdentityID, touchLabel(ctx, store, label.ID, now)
}

// touchLabel moves the Label's updated_at; a missing Label answers not
// found.
func touchLabel(ctx context.Context, store Store, labelID string, now time.Time) error {
	if err := store.TouchLabel(ctx, labelID, now); err != nil {
		if errors.Is(err, ErrLabelNotFound) {
			return domainerr.ResourceNotFound("label", labelID)
		}
		return err
	}
	return nil
}

func labelDeletionRevision(rows []DeletionImpact) string {
	hash := sha256.New()
	for _, row := range rows {
		_, _ = fmt.Fprintf(hash, "%d\x00%s\n", row.Kind, row.EntityID)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// DeletionPreview is what deleting a Label would detach, with the
// revision DeleteLabel checks.
type DeletionPreview struct {
	Revision string
	Impacts  []DeletionImpact
}

// PreviewDeleteLabelQuery reads what deleting a Label would detach.
type PreviewDeleteLabelQuery struct {
	ID string
}

// requireLabelExists answers a missing Label not found before its
// authorization.
func (s *Service) requireLabelExists(ctx context.Context, labelID string) error {
	err := s.units.Read(ctx, func(store Store) error {
		_, err := store.FindLabelID(ctx, labelID)
		return err
	})
	if errors.Is(err, ErrLabelNotFound) {
		return domainerr.ResourceNotFound("label", labelID)
	}
	return err
}

// PreviewDeleteLabel lists the child Labels, Artists, Program Events and
// Releases that reference the Label, with the revision DeleteLabel checks.
func (s *Service) PreviewDeleteLabel(ctx context.Context, query PreviewDeleteLabelQuery) (_ DeletionPreview, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", query.ID))

	if err := s.requireLabelExists(ctx, query.ID); err != nil {
		return DeletionPreview{}, err
	}
	if err := labelEntity.RequirePermission(ctx, s.spiceDB, query.ID, policyv1.Label.Delete); err != nil {
		return DeletionPreview{}, err
	}
	var rows []DeletionImpact
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		rows, err = store.DeletionImpacts(ctx, query.ID)
		return err
	})
	if err != nil {
		return DeletionPreview{}, err
	}
	return DeletionPreview{Revision: labelDeletionRevision(rows), Impacts: rows}, nil
}

// DeleteLabelCommand deletes a Label whose usage still matches the preview
// the caller reviewed.
type DeleteLabelCommand struct {
	ID              string
	PreviewRevision string
}

// labelDeletion is the SpiceDB snapshot a Label deletion removes.
type labelDeletion struct {
	deleted  []policyv1.RelationshipMutation
	restored []policyv1.RelationshipMutation
}

// DeleteLabel deletes the Label, its content document, share links, logo
// bindings, OG image and SpiceDB relationships.
func (s *Service) DeleteLabel(ctx context.Context, command DeleteLabelCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", command.ID))

	if strings.TrimSpace(command.PreviewRevision) == "" {
		return domainerr.Precondition("delete preview is required")
	}
	if err := s.requireLabelExists(ctx, command.ID); err != nil {
		return err
	}
	snapshotPlan, err := policyv1.Label.Snapshot(command.ID)
	if err != nil {
		return domainerr.InvalidField("id", "must identify a Label")
	}
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[labelDeletion, Store]{
		Lock: func(ctx context.Context, store Store) error {
			// Freeze every relation represented by the preview revision until
			// the Label delete commits. Deletion is rare; one short table lock
			// keeps the exact-impact CAS honest without adding cross-domain
			// state.
			if err := store.LockLabelRelations(ctx); err != nil {
				return err
			}
			_, err := store.LockLabelID(ctx, command.ID)
			return err
		},
		Access: labelEntity.LockedAccess(s.spiceDB, command.ID, policyv1.Label.Delete),
		Change: func(ctx context.Context, store Store) (labelDeletion, error) {
			return s.deleteLabel(ctx, store, command, snapshotPlan)
		},
		Audit: func(labelDeletion) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditLabelDeleted, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewLabelDeletedAuditRecord(m, command.ID)
			})
		},
		Relationships: func(deletion labelDeletion) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return deletion.deleted, deletion.restored, nil
		},
	})
	return err
}

func (s *Service) deleteLabel(ctx context.Context, store Store, command DeleteLabelCommand, snapshotPlan policyv1.RelationshipSnapshotPlan) (labelDeletion, error) {
	impacts, err := store.DeletionImpacts(ctx, command.ID)
	if err != nil {
		return labelDeletion{}, err
	}
	if labelDeletionRevision(impacts) != command.PreviewRevision {
		return labelDeletion{}, domainerr.Precondition("label usage changed; review deletion impact again")
	}
	snapshots, _, err := s.spiceDB.SnapshotResourceRelationshipDescriptors(ctx, snapshotPlan)
	if err != nil {
		return labelDeletion{}, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	deleted, restored, err := labelEntity.DeletionRelationshipMutations(command.ID, snapshots)
	if err != nil {
		return labelDeletion{}, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if err := validateResourceDeletionAuthorizationBatchSize("label", deleted, restored); err != nil {
		return labelDeletion{}, err
	}
	if err := store.DeleteShareLinks(ctx, command.ID); err != nil {
		return labelDeletion{}, err
	}
	if err := store.CancelOG(ctx, command.ID); err != nil {
		return labelDeletion{}, err
	}
	if err := store.ReleaseLogoBindings(ctx, command.ID); err != nil {
		return labelDeletion{}, err
	}
	documentID, err := loadLabelContentDocumentID(ctx, store, command.ID)
	if err != nil {
		return labelDeletion{}, err
	}
	if err := store.DeleteContentDocument(ctx, documentID, labelContentDocumentFence(command.ID)); err != nil {
		return labelDeletion{}, normalizeLabelContentBlockError(err)
	}
	if err := store.DeleteLabel(ctx, command.ID); err != nil {
		if errors.Is(err, ErrLabelNotFound) {
			return labelDeletion{}, domainerr.ResourceNotFound("label", command.ID)
		}
		return labelDeletion{}, err
	}
	return labelDeletion{deleted: deleted, restored: restored}, nil
}

func validateResourceDeletionAuthorizationBatchSize(resourceName string, deleted, restored []policyv1.RelationshipMutation) error {
	const maxMutations = 1000
	if len(deleted) <= maxMutations && len(restored) <= maxMutations {
		return nil
	}
	return domainerr.Precondition(fmt.Sprintf("%s has too many authorization relationships to delete atomically; remove participant relationships or reparent dependent resources first", resourceName))
}

// PublishLabelCommand publishes a draft Label.
type PublishLabelCommand struct {
	ID string
}

// UnpublishLabelCommand returns a published Label to draft.
type UnpublishLabelCommand struct {
	ID string
}

// LifecycleResult is the Label's lifecycle after a publish or unpublish.
type LifecycleResult struct {
	ID          string
	Changed     bool
	Status      Status
	PublishedAt *time.Time
	UpdatedAt   time.Time
}

// PublishLabel publishes the Label; a published Label is left unchanged.
func (s *Service) PublishLabel(ctx context.Context, command PublishLabelCommand) (_ LifecycleResult, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", command.ID))
	return s.transitionLabel(ctx, command.ID, labelTransition{
		target: StatusPublished, previous: sharedtelemetry.AuditStateDraft, next: sharedtelemetry.AuditStatePublished,
		stampsPublication: true,
		apply: func(label *Label, now time.Time) {
			label.Status = StatusPublished
			label.PublishedAt = &now
			label.UpdatedAt = now
		},
	})
}

// UnpublishLabel returns the Label to draft; a draft Label is left unchanged.
func (s *Service) UnpublishLabel(ctx context.Context, command UnpublishLabelCommand) (_ LifecycleResult, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", command.ID))
	return s.transitionLabel(ctx, command.ID, labelTransition{
		target: StatusDraft, previous: sharedtelemetry.AuditStatePublished, next: sharedtelemetry.AuditStateDraft,
		apply: func(label *Label, now time.Time) {
			label.Status = StatusDraft
			label.UpdatedAt = now
		},
	})
}

// labelTransition moves a Label to target, stamping published_at when
// stampsPublication is set; apply mirrors the stored change on the locked
// row for the response.
type labelTransition struct {
	target            Status
	previous, next    sharedtelemetry.AuditState
	stampsPublication bool
	apply             func(*Label, time.Time)
}

// transitionLabel locks the Label row before the fresh Publish check, so a
// role revoked while the transition waited on the row is observed.
func (s *Service) transitionLabel(ctx context.Context, id string, transition labelTransition) (LifecycleResult, error) {
	var label Label
	now := dbtime.Now()
	changed, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[bool, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			label, err = store.LockLabel(ctx, id)
			return err
		},
		Access: labelEntity.LockedAccess(s.spiceDB, id, policyv1.Label.Publish),
		Change: func(ctx context.Context, store Store) (bool, error) {
			if label.Status == transition.target {
				return false, nil
			}
			var publishedAt *time.Time
			if transition.stampsPublication {
				publishedAt = &now
			}
			return true, store.SetLabelStatus(ctx, id, transition.target, publishedAt, now)
		},
		Audit: func(changed bool) mutation.Audit {
			if !changed {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditLabelUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewLabelLifecycleAuditRecord(m, label.ID, transition.previous, transition.next)
			})
		},
		Outbox: func(ctx context.Context, store Store, changed bool) error {
			if !changed {
				return nil
			}
			return store.PublishContentUpdate(ctx, ContentUpdate{LabelID: label.ID, Kind: StateUpdate})
		},
	})
	if err != nil {
		if errors.Is(err, ErrLabelNotFound) {
			return LifecycleResult{}, domainerr.ResourceNotFound("label", id)
		}
		return LifecycleResult{}, err
	}
	if changed {
		transition.apply(&label, now)
	}
	return LifecycleResult{
		ID: label.ID, Changed: changed, Status: label.Status,
		PublishedAt: label.PublishedAt, UpdatedAt: label.UpdatedAt,
	}, nil
}
