package page

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/proto"
)

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

func (s *PageService) preparePageUpdate(
	ctx context.Context,
	request *managev1.UpdatePageRequest,
) (pageUpdate, error) {
	normalizedSlug, slugPresent := normalizeOptionalNullableString(request.Slug)
	if slugPresent && normalizedSlug != nil {
		if err := s.checkSlugAvailable(ctx, *normalizedSlug, request.Id); err != nil {
			return pageUpdate{}, err
		}
	}
	update := pageUpdate{
		normalizedSlug: normalizedSlug,
		slugPresent:    slugPresent,
		showTitle:      request.ShowTitle,
	}
	if request.AccessPolicy != nil {
		policy, err := pageaccess.Normalize(request.AccessPolicy)
		if err != nil {
			return pageUpdate{}, errs.InvalidArgument("access_policy", err.Error())
		}
		encoded, err := pageaccess.Encode(policy)
		if err != nil {
			return pageUpdate{}, errs.Internal(err)
		}
		update.accessPolicy = policy
		update.encodedAccessPolicy = encoded
	}
	return update, nil
}

// applyPageUpdate commits the Page configuration change and, when it changed,
// schedules event in the same transaction.
func (s *PageService) applyPageUpdate(
	ctx context.Context,
	page *model.Page,
	update pageUpdate,
	event *managev1.ContentUpdatedEvent,
) (bool, error) {
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lockedPage, err := lockPageMenuTargetStateForUpdate(ctx, tx, page.ID)
		if err != nil {
			return err
		}
		if err := requireLockedPagePermission(ctx, tx, s.spiceDB, page.ID, policyv1.Page.Manage); err != nil {
			return err
		}
		if err := validatePageUpdateSlug(ctx, tx, page.ID, update); err != nil {
			return err
		}
		if err := validatePageAccessPolicyTags(ctx, tx, update.accessPolicy); err != nil {
			return err
		}
		currentPolicy, err := pageaccess.Decode(lockedPage.AccessPolicy)
		if err != nil {
			return errs.Internal(err)
		}
		// Keep the root value read under FOR UPDATE as an immutable rewrite
		// input. The Page row update below must never influence which legacy
		// slug-only Menu targets are selected for the same transaction.
		previousSlug := cloneOptionalString(lockedPage.Slug)
		page.Slug = cloneOptionalString(previousSlug)
		page.ShowTitle = lockedPage.ShowTitle
		page.UpdatedAt = lockedPage.UpdatedAt
		page.AccessPolicy = lockedPage.AccessPolicy
		changedFields := pageUpdateChangedFields(lockedPage, update)
		if update.accessPolicy != nil && !proto.Equal(currentPolicy, update.accessPolicy) {
			changedFields = append(changedFields, "access_policy")
		}
		if len(changedFields) == 0 {
			return nil
		}
		// PostgreSQL keeps microseconds; respond with the value later reads return.
		mutationNow := dbtime.Now()
		if err := tx.Model(page).Updates(update.columns(mutationNow)).Error; err != nil {
			return err
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
		if err := s.updatePageMenuTargetAfterSlugChange(ctx, tx, page.ID, previousSlug, update); err != nil {
			return err
		}
		changed = true
		if s.auditWriter != nil {
			if err := domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPageConfigurationAuditRecord(metadata, page.ID, changedFields)
			}); err != nil {
				return err
			}
		}
		return publishContentUpdatedEvent(ctx, tx, s.asyncPublisher, event)
	})
	return changed, err
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
		return errs.Internal(err)
	}
	if len(ids) != len(policy.UserTagIds) {
		return errs.InvalidArgument("access_policy.user_tag_ids", "selected user tag does not exist")
	}
	return nil
}

func validatePageUpdateSlug(ctx context.Context, tx *gorm.DB, pageID string, update pageUpdate) error {
	if !update.slugPresent || update.normalizedSlug == nil {
		return nil
	}
	slug := *update.normalizedSlug
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
		return errs.SlugAlreadyExists("page", slug)
	}
	return nil
}

func (s *PageService) updatePageMenuTargetAfterSlugChange(
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
	if s.menuTargets == nil {
		return errs.InternalMsg("Page Menu target slug updater is not configured")
	}
	return s.menuTargets.UpdateSlug(ctx, tx, "page", pageID, derefString(previousSlug), nextTarget)
}

func normalizePageUpdateError(err error) error {
	if dberrors.IsUniqueViolation(err) {
		return errs.WithCause(errs.SlugAlreadyExists("page", "slug"), err)
	}
	return errs.Internal(err)
}
