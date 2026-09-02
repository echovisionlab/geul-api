package page

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type pageUpdate struct {
	fields         structured.Fields
	normalizedSlug *string
	slugPresent    bool
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
		fields:         structured.Fields{},
		normalizedSlug: normalizedSlug,
		slugPresent:    slugPresent,
	}
	if slugPresent {
		update.fields["slug"] = nil
		if normalizedSlug != nil {
			update.fields["slug"] = *normalizedSlug
		}
	}
	if request.ShowTitle != nil {
		update.fields["show_title"] = *request.ShowTitle
	}
	return update, nil
}

func (s *PageService) applyPageUpdate(ctx context.Context, page *model.Page, update pageUpdate) (bool, error) {
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
		// Keep the root value read under FOR UPDATE as an immutable rewrite
		// input. The Page row update below must never influence which legacy
		// slug-only Menu targets are selected for the same transaction.
		previousSlug := cloneOptionalString(lockedPage.Slug)
		page.Slug = cloneOptionalString(previousSlug)
		page.ShowTitle = lockedPage.ShowTitle
		page.UpdatedAt = lockedPage.UpdatedAt
		changedFields := pageUpdateChangedFields(lockedPage, update)
		if len(changedFields) == 0 {
			return nil
		}
		mutationNow := time.Now()
		update.fields["updated_at"] = mutationNow
		if err := tx.Model(page).Updates(update.fields).Error; err != nil {
			return err
		}
		if update.slugPresent {
			page.Slug = update.normalizedSlug
		}
		if showTitle, ok := update.fields["show_title"].(bool); ok {
			page.ShowTitle = showTitle
		}
		page.UpdatedAt = mutationNow
		if err := s.updatePageMenuTargetAfterSlugChange(ctx, tx, page.ID, previousSlug, update); err != nil {
			return err
		}
		changed = true
		if s.auditWriter == nil {
			return nil
		}
		return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPageConfigurationAuditRecord(metadata, page.ID, changedFields)
		})
	})
	return changed, err
}

func pageUpdateChangedFields(page *lockedPageMenuTargetState, update pageUpdate) []string {
	fields := []string{}
	if update.slugPresent && !nullableStringEqual(page.Slug, update.normalizedSlug) {
		fields = append(fields, "slug")
	}
	if value, ok := update.fields["show_title"].(bool); ok && value != page.ShowTitle {
		fields = append(fields, "show_title")
	}
	return fields
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
	if strings.Contains(err.Error(), "duplicate key") {
		return errs.SlugAlreadyExists("page", "slug")
	}
	if connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	return errs.Internal(err)
}
