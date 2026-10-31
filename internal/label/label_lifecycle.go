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

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

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
func (s *Service) CreateLabel(ctx context.Context, command CreateLabelCommand) (*managev1.Label, error) {
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, errAuthenticationRequired
	}
	name := strings.TrimSpace(command.Name)
	if name == "" {
		return nil, domainerr.Required("name")
	}
	if err := s.validateNewLabelSlug(ctx, command.Slug); err != nil {
		return nil, err
	}
	label := newLabel(command)
	ownerMemberID := user.MemberID.String()
	var ownerIdentityID string
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		// A new Label has no resource row to lock yet. The fresh check locks
		// the canonical principal before the first durable state change and
		// keeps that lock for the rest of the transaction.
		Access: mutation.FreshAdminCan(policyv1.Label.Create()),
		Change: func(ctx context.Context, tx *gorm.DB) (string, error) {
			var err error
			ownerIdentityID, err = s.createLabelWithDB(ctx, tx, &label, name, command.Document, ownerMemberID, command.AcceptLanguage)
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
		return nil, err
	}
	if err := s.overlayLabelSourceLocaleDocument(ctx, &label); err != nil {
		return nil, err
	}
	return s.labelWithReadyOg(ctx, &label, nil)
}

func (s *Service) validateNewLabelSlug(ctx context.Context, slug *string) error {
	if slug == nil || *slug == "" {
		return nil
	}
	if err := s.checkSlugAvailable(ctx, *slug, ""); err != nil {
		return err
	}
	return routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "label", "labels", *slug)
}

func newLabel(command CreateLabelCommand) model.Label {
	label := model.Label{
		Status: managev1.LabelStatus_LABEL_STATUS_DRAFT.String(),
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

// createLabelWithDB stores label with its content document, owner and
// source-locale title, and returns the owner's Identity.
func (s *Service) createLabelWithDB(
	ctx context.Context,
	tx *gorm.DB,
	label *model.Label,
	name string,
	documentInput *contentv1.RichTextDocument,
	ownerMemberID string,
	acceptLanguage string,
) (string, error) {
	if err := requireLabelRouteAvailableWithDB(ctx, tx, label.Slug); err != nil {
		return "", err
	}
	sourceLocale := s.translation.ResolveInitialSourceLocale(ctx, tx, acceptLanguage)
	label.SourceLocale = sourceLocale
	document, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
		Profile:      creativeContentProfile,
		SourceLocale: sourceLocale,
	})
	if err != nil {
		return "", normalizeLabelContentBlockError(err)
	}
	contentDocumentID := document.Document.ID.String()
	label.ContentDocumentID = &contentDocumentID
	if err := tx.Clauses(clause.Returning{Columns: []clause.Column{
		{Name: "id"}, {Name: "created_at"}, {Name: "updated_at"},
	}}).Create(label).Error; err != nil {
		if dberrors.IsUniqueViolation(err) {
			return "", domainerr.New(domainerr.AlreadyExists, "", "label with this slug already exists").WithCause(err)
		}
		return "", err
	}
	if label.ParentLabelID != nil {
		if err := labelEntity.ValidateParentTransition(ctx, tx, label.ID, *label.ParentLabelID); err != nil {
			return "", err
		}
	}
	if err := insertLabelOwner(tx, label.ID, ownerMemberID); err != nil {
		return "", err
	}
	owner, err := authorizationtarget.RequireLocked(ctx, tx, ownerMemberID)
	if err != nil {
		return "", err
	}
	if _, err := initializeCreativeContentDocument(ctx, tx, s.contentBlocks, label.ID, sourceLocale, document, documentInput); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	if err := saveLabelSourceLocaleDocumentState(ctx, tx, label.ID, sourceLocale, translationLocaleDocumentSaveInput{
		Title: &name, OverwriteNullFields: true, Now: now,
	}); err != nil {
		return "", err
	}
	return owner.IdentityID, touchLabelRootUpdatedAt(ctx, tx, label.ID, now)
}

func requireLabelRouteAvailableWithDB(ctx context.Context, tx *gorm.DB, slug *string) error {
	if slug == nil || *slug == "" {
		return nil
	}
	return routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "label", "labels", *slug)
}

func insertLabelOwner(tx *gorm.DB, labelID, memberID string) error {
	return tx.Exec(`
		INSERT INTO label_owner (label_id, member_id, created_at)
		VALUES (?::uuid, ?::uuid, now())
	`, labelID, memberID).Error
}

type labelDeletionImpactRow struct {
	Kind        int32  `gorm:"column:kind"`
	EntityID    string `gorm:"column:entity_id"`
	DisplayName string `gorm:"column:display_name"`
}

func loadLabelDeletionImpacts(ctx context.Context, db *gorm.DB, labelID string) ([]labelDeletionImpactRow, error) {
	var rows []labelDeletionImpactRow
	query := fmt.Sprintf(`
		SELECT 1::integer AS kind, child.id::text AS entity_id, %s AS display_name
		FROM label AS child
		WHERE child.parent_label_id = ?::uuid
		UNION ALL
		SELECT 2::integer, artist.id::text, %s
		FROM artist_label AS relation
		JOIN artist ON artist.id = relation.artist_id
		WHERE relation.label_id = ?::uuid
		UNION ALL
		SELECT 3::integer, event.id::text, COALESCE(NULLIF(event.title, ''), event.slug)
		FROM program_event_label AS relation
		JOIN program_event AS event ON event.id = relation.event_id
		WHERE relation.label_id = ?::uuid
		UNION ALL
		SELECT 4::integer, release.id::text, %s
		FROM release_label AS relation
		JOIN release ON release.id = relation.release_id
		WHERE relation.label_id = ?::uuid
		ORDER BY 1, 2
	`, LabelSourceTitleSQL("child"), ArtistSourceTitleSQL("artist"), ReleaseSourceTitleSQL("release"))
	if err := db.WithContext(ctx).Raw(query, labelID, labelID, labelID, labelID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func labelDeletionRevision(rows []labelDeletionImpactRow) string {
	hash := sha256.New()
	for _, row := range rows {
		_, _ = fmt.Fprintf(hash, "%d\x00%s\n", row.Kind, row.EntityID)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func labelDeletionPreview(rows []labelDeletionImpactRow) *managev1.PreviewDeleteLabelResponse {
	response := &managev1.PreviewDeleteLabelResponse{Revision: labelDeletionRevision(rows)}
	for _, row := range rows {
		kind := managev1.LabelDeletionImpactKind(row.Kind)
		response.Impacts = append(response.Impacts, &managev1.LabelDeletionImpact{
			Kind: kind, EntityId: row.EntityID, DisplayName: row.DisplayName,
		})
		switch kind {
		case managev1.LabelDeletionImpactKind_LABEL_DELETION_IMPACT_KIND_CHILD_LABEL:
			response.ChildLabelCount++
		case managev1.LabelDeletionImpactKind_LABEL_DELETION_IMPACT_KIND_ARTIST:
			response.ArtistCount++
		case managev1.LabelDeletionImpactKind_LABEL_DELETION_IMPACT_KIND_PROGRAM_EVENT:
			response.ProgramEventCount++
		case managev1.LabelDeletionImpactKind_LABEL_DELETION_IMPACT_KIND_RELEASE:
			response.ReleaseCount++
		}
	}
	return response
}

// PreviewDeleteLabelQuery reads what deleting a Label would detach.
type PreviewDeleteLabelQuery struct {
	ID string
}

// PreviewDeleteLabel lists the child Labels, Artists, Program Events and
// Releases that reference the Label, with the revision DeleteLabel checks.
func (s *Service) PreviewDeleteLabel(ctx context.Context, query PreviewDeleteLabelQuery) (_ *managev1.PreviewDeleteLabelResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", query.ID))

	var label model.Label
	if err := s.db.WithContext(ctx).Select("id").First(&label, "id = ?", query.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("label", query.ID)
		}
		return nil, err
	}
	if err := labelEntity.RequirePermission(ctx, s.spiceDB, query.ID, policyv1.Label.Delete); err != nil {
		return nil, err
	}
	rows, err := loadLabelDeletionImpacts(ctx, s.db, query.ID)
	if err != nil {
		return nil, err
	}
	return labelDeletionPreview(rows), nil
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
	var label model.Label
	if err := s.db.WithContext(ctx).
		Select("id").
		First(&label, "id = ?", command.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("label", command.ID)
		}
		return err
	}
	snapshotPlan, err := policyv1.Label.Snapshot(command.ID)
	if err != nil {
		return domainerr.InvalidField("id", "must identify a Label")
	}
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[labelDeletion]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			// Freeze every relation represented by the preview revision until
			// the Label delete commits. Deletion is rare; one short table lock
			// keeps the exact-impact CAS honest without adding cross-domain
			// state.
			if err := tx.Exec(`
				LOCK TABLE label, artist_label, program_event_label, release_label
				IN SHARE ROW EXCLUSIVE MODE
			`).Error; err != nil {
				return err
			}
			return tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
				Select("id").
				First(&label, "id = ?", command.ID).Error
		},
		Access: labelEntity.LockedAccess(s.spiceDB, command.ID, policyv1.Label.Delete),
		Change: func(ctx context.Context, tx *gorm.DB) (labelDeletion, error) {
			return s.deleteLabelWithDB(ctx, tx, command, snapshotPlan)
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

func (s *Service) deleteLabelWithDB(ctx context.Context, tx *gorm.DB, command DeleteLabelCommand, snapshotPlan policyv1.RelationshipSnapshotPlan) (labelDeletion, error) {
	impacts, err := loadLabelDeletionImpacts(ctx, tx, command.ID)
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
	if err := tx.
		Where("entity_type = ? AND entity_id = ?", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_LABEL.String(), command.ID).
		Delete(&model.ShareLink{}).Error; err != nil {
		return labelDeletion{}, err
	}
	if err := s.runtime.CancelAndReleaseOGWithDB(ctx, tx, command.ID); err != nil {
		return labelDeletion{}, err
	}
	if err := s.runtime.ReleaseExactPublicAssetBindings(ctx, tx, "label", command.ID, []string{"logo_light", "logo_dark"}); err != nil {
		return labelDeletion{}, err
	}
	documentID, err := loadLabelContentDocumentID(ctx, tx, command.ID)
	if err != nil {
		return labelDeletion{}, err
	}
	if err := s.contentBlocks.DeleteDocument(ctx, tx, documentID, labelContentDocumentFence(command.ID)); err != nil {
		return labelDeletion{}, normalizeLabelContentBlockError(err)
	}
	result := tx.Delete(&model.Label{}, "id = ?", command.ID)
	if result.Error != nil {
		return labelDeletion{}, result.Error
	}
	if result.RowsAffected == 0 {
		return labelDeletion{}, domainerr.ResourceNotFound("label", command.ID)
	}
	return labelDeletion{deleted: deleted, restored: restored}, nil
}

// PublishLabelCommand publishes a draft Label.
type PublishLabelCommand struct {
	ID string
}

// UnpublishLabelCommand returns a published Label to draft.
type UnpublishLabelCommand struct {
	ID string
}

// PublishLabel publishes the Label; a published Label is left unchanged.
func (s *Service) PublishLabel(ctx context.Context, command PublishLabelCommand) (_ *managev1.LabelLifecycleMutationResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", command.ID))
	return s.transitionLabel(ctx, command.ID, labelTransition{
		target: managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String(), previous: sharedtelemetry.AuditStateDraft, next: sharedtelemetry.AuditStatePublished,
		fields: func(now time.Time) structured.Fields {
			return structured.Fields{"status": managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String(), "published_at": now, "updated_at": now}
		},
		apply: func(label *model.Label, now time.Time) {
			label.Status = managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String()
			label.PublishedAt = &now
			label.UpdatedAt = now
		},
	})
}

// UnpublishLabel returns the Label to draft; a draft Label is left unchanged.
func (s *Service) UnpublishLabel(ctx context.Context, command UnpublishLabelCommand) (_ *managev1.LabelLifecycleMutationResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", command.ID))
	return s.transitionLabel(ctx, command.ID, labelTransition{
		target: managev1.LabelStatus_LABEL_STATUS_DRAFT.String(), previous: sharedtelemetry.AuditStatePublished, next: sharedtelemetry.AuditStateDraft,
		fields: func(now time.Time) structured.Fields {
			return structured.Fields{"status": managev1.LabelStatus_LABEL_STATUS_DRAFT.String(), "updated_at": now}
		},
		apply: func(label *model.Label, now time.Time) {
			label.Status = managev1.LabelStatus_LABEL_STATUS_DRAFT.String()
			label.UpdatedAt = now
		},
	})
}

// labelTransition moves a Label to target; fields are the stored columns
// and apply mirrors them on the locked row for the response.
type labelTransition struct {
	target         string
	previous, next sharedtelemetry.AuditState
	fields         func(time.Time) structured.Fields
	apply          func(*model.Label, time.Time)
}

// transitionLabel locks the Label row before the fresh Publish check, so a
// role revoked while the transition waited on the row is observed.
func (s *Service) transitionLabel(ctx context.Context, id string, transition labelTransition) (*managev1.LabelLifecycleMutationResponse, error) {
	var label model.Label
	now := dbtime.Now()
	changed, err := mutation.Run(ctx, s.mutations, mutation.Spec[bool]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&label, "id = ?", id).Error
		},
		Access: labelEntity.LockedAccess(s.spiceDB, id, policyv1.Label.Publish),
		Change: func(_ context.Context, tx *gorm.DB) (bool, error) {
			if label.Status == transition.target {
				return false, nil
			}
			return true, tx.Model(&label).Updates(transition.fields(now)).Error
		},
		Audit: func(changed bool) mutation.Audit {
			if !changed {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditLabelUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewLabelLifecycleAuditRecord(m, label.ID, transition.previous, transition.next)
			})
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, changed bool) error {
			if !changed {
				return nil
			}
			return contentsignal.PublishInTransaction(ctx, tx, s.publisher, buildLabelStateTransitionContentUpdatedEvent(label.ID))
		},
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("label", id)
		}
		return nil, err
	}
	if changed {
		transition.apply(&label, now)
	}
	return &managev1.LabelLifecycleMutationResponse{
		Id: label.ID, Changed: changed,
		Status:      managev1.LabelStatus(managev1.LabelStatus_value[label.Status]),
		PublishedAt: timestampProtoPtr(label.PublishedAt), UpdatedAt: timestamppb.New(label.UpdatedAt),
	}, nil
}
