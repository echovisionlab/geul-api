package page

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// UpdatePageCommand changes the Page settings it sets. A present Slug that
// trims to empty clears the slug; AccessPolicy replaces the whole audience.
type UpdatePageCommand struct {
	ID           string
	Slug         *string
	ShowTitle    *bool
	AccessPolicy *commonv1.PageAccessPolicy
}

// pageUpdate is a validated Page configuration update. Each field is set
// only when the request sets it; slugPresent with a nil normalizedSlug clears
// the slug.
type pageUpdate struct {
	normalizedSlug      *string
	slugPresent         bool
	showTitle           *bool
	accessPolicy        *commonv1.PageAccessPolicy
	encodedAccessPolicy json.RawMessage
}

// columns is the Page row update for the fields the request sets.
func (u pageUpdate) columns(updatedAt time.Time) structured.Fields {
	columns := structured.Fields{"updated_at": updatedAt}
	if u.slugPresent {
		columns["slug"] = nil
		if u.normalizedSlug != nil {
			columns["slug"] = *u.normalizedSlug
		}
	}
	if u.showTitle != nil {
		columns["show_title"] = *u.showTitle
	}
	if u.accessPolicy != nil {
		columns["access_policy"] = u.encodedAccessPolicy
	}
	return columns
}

// pageUpdated is the Page settings an update leaves behind.
type pageUpdated struct {
	page          model.Page
	changedFields []string
}

// UpdatePage changes the Page slug, title visibility or reader audience.
func (s *Service) UpdatePage(ctx context.Context, command UpdatePageCommand) (_ *managev1.UpdatePageResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.ID))

	update, err := s.preparePageUpdate(ctx, command)
	if err != nil {
		return nil, err
	}
	updated, err := mutation.Run(ctx, s.mutations, mutation.Spec[pageUpdated]{
		Access: s.lockedPage(command.ID, policyv1.Page.Manage),
		Change: func(ctx context.Context, tx *gorm.DB) (pageUpdated, error) {
			return s.applyPageUpdate(ctx, tx, command.ID, update)
		},
		Audit: func(updated pageUpdated) mutation.Audit {
			if len(updated.changedFields) == 0 {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPageConfigurationAuditRecord(metadata, command.ID, updated.changedFields)
			})
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, updated pageUpdated) error {
			if len(updated.changedFields) == 0 {
				return nil
			}
			return s.publish(ctx, tx, buildManagePageContentUpdatedEvent(command))
		},
	})
	if err != nil {
		if dberrors.IsUniqueViolation(err) {
			return nil, domainerr.Duplicate("page", "slug", "slug").WithCause(err)
		}
		return nil, err
	}
	page := updated.page
	accessPolicy, err := pageaccess.Decode(page.AccessPolicy)
	if err != nil {
		return nil, err
	}
	return &managev1.UpdatePageResponse{
		Id:           page.ID,
		Changed:      len(updated.changedFields) != 0,
		Slug:         page.Slug,
		ShowTitle:    page.ShowTitle,
		UpdatedAt:    timestamppb.New(page.UpdatedAt),
		AccessPolicy: accessPolicy,
	}, nil
}

func (s *Service) preparePageUpdate(ctx context.Context, command UpdatePageCommand) (pageUpdate, error) {
	normalizedSlug, slugPresent := normalizeOptionalNullableString(command.Slug)
	if slugPresent && normalizedSlug != nil {
		if err := s.checkSlugAvailable(ctx, *normalizedSlug, command.ID); err != nil {
			return pageUpdate{}, err
		}
	}
	update := pageUpdate{
		normalizedSlug: normalizedSlug,
		slugPresent:    slugPresent,
		showTitle:      command.ShowTitle,
	}
	if command.AccessPolicy != nil {
		policy, err := pageaccess.Normalize(command.AccessPolicy)
		if err != nil {
			return pageUpdate{}, domainerr.InvalidField("access_policy", err.Error())
		}
		encoded, err := pageaccess.Encode(policy)
		if err != nil {
			return pageUpdate{}, err
		}
		update.accessPolicy = policy
		update.encodedAccessPolicy = encoded
	}
	return update, nil
}

// applyPageUpdate writes the Page configuration change and reports the
// fields it changed; none means the Page already had these settings.
func (s *Service) applyPageUpdate(ctx context.Context, tx *gorm.DB, pageID string, update pageUpdate) (pageUpdated, error) {
	lockedPage, err := lockPageMenuTargetStateForUpdate(ctx, tx, pageID)
	if err != nil {
		return pageUpdated{}, err
	}
	if update.slugPresent && update.normalizedSlug != nil {
		if err := lockAvailablePageSlug(ctx, tx, *update.normalizedSlug, pageID); err != nil {
			return pageUpdated{}, err
		}
	}
	if err := validatePageAccessPolicyTags(ctx, tx, update.accessPolicy); err != nil {
		return pageUpdated{}, err
	}
	currentPolicy, err := pageaccess.Decode(lockedPage.AccessPolicy)
	if err != nil {
		return pageUpdated{}, err
	}
	// Keep the root value read under FOR UPDATE as an immutable rewrite
	// input. The Page row update below must never influence which legacy
	// slug-only Menu targets are selected for the same transaction.
	previousSlug := cloneOptionalString(lockedPage.Slug)
	updated := pageUpdated{page: model.Page{
		ID: pageID, Slug: cloneOptionalString(previousSlug), ShowTitle: lockedPage.ShowTitle,
		UpdatedAt: lockedPage.UpdatedAt, AccessPolicy: lockedPage.AccessPolicy,
	}}
	updated.changedFields = pageUpdateChangedFields(lockedPage, update)
	if update.accessPolicy != nil && !proto.Equal(currentPolicy, update.accessPolicy) {
		updated.changedFields = append(updated.changedFields, "access_policy")
	}
	if len(updated.changedFields) == 0 {
		return updated, nil
	}
	// PostgreSQL keeps microseconds; respond with the value later reads return.
	mutationNow := dbtime.Now()
	page := &updated.page
	if err := tx.Model(page).Updates(update.columns(mutationNow)).Error; err != nil {
		return pageUpdated{}, err
	}
	if update.slugPresent {
		page.Slug = update.normalizedSlug
	}
	if update.showTitle != nil {
		page.ShowTitle = *update.showTitle
	}
	page.UpdatedAt = mutationNow
	if update.accessPolicy != nil {
		page.AccessPolicy = update.encodedAccessPolicy
	}
	if err := s.updatePageMenuTargetAfterSlugChange(ctx, tx, pageID, previousSlug, update); err != nil {
		return pageUpdated{}, err
	}
	return updated, nil
}

func pageUpdateChangedFields(page *lockedPageMenuTargetState, update pageUpdate) []string {
	fields := []string{}
	if update.slugPresent && !nullableStringEqual(page.Slug, update.normalizedSlug) {
		fields = append(fields, "slug")
	}
	if update.showTitle != nil && *update.showTitle != page.ShowTitle {
		fields = append(fields, "show_title")
	}
	return fields
}

func validatePageAccessPolicyTags(ctx context.Context, tx *gorm.DB, policy *commonv1.PageAccessPolicy) error {
	if policy == nil || len(policy.UserTagIds) == 0 {
		return nil
	}
	var ids []string
	if err := tx.WithContext(ctx).Model(&model.UserTag{}).
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Where("id IN ?", policy.UserTagIds).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) != len(policy.UserTagIds) {
		return domainerr.InvalidField("access_policy.user_tag_ids", "selected user tag does not exist")
	}
	return nil
}

// lockAvailablePageSlug locks the route of slug and rejects a slug another
// Page or resource route holds.
func lockAvailablePageSlug(ctx context.Context, tx *gorm.DB, slug, pageID string) error {
	if err := routeregistry.LockPageRouteConflict(ctx, tx, slug); err != nil {
		return err
	}
	if err := ensureSlugAvailable(ctx, tx, &model.Page{}, "page", slug, pageID); err != nil {
		return err
	}
	occupied, err := routeregistry.IsPageRouteOccupiedByResource(ctx, tx, slug)
	if err != nil {
		return err
	}
	if occupied {
		return domainerr.Duplicate("page", "slug", slug)
	}
	return nil
}

func (s *Service) updatePageMenuTargetAfterSlugChange(
	ctx context.Context,
	tx *gorm.DB,
	pageID string,
	previousSlug *string,
	update pageUpdate,
) error {
	if !update.slugPresent || nullableStringEqual(previousSlug, update.normalizedSlug) {
		return nil
	}
	nextTarget := pageID
	if update.normalizedSlug != nil {
		nextTarget = *update.normalizedSlug
	}
	return s.menuTargets.UpdateSlug(ctx, tx, "page", pageID, derefString(previousSlug), nextTarget)
}
