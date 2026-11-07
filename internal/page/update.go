package page

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// UpdatePageCommand changes the Page settings it sets. A present Slug that
// trims to empty clears the slug; AccessPolicy replaces the whole policy.
type UpdatePageCommand struct {
	ID           string
	Slug         *string
	ShowTitle    *bool
	AccessPolicy *pageaccess.Policy
}

// pageUpdate is a validated Page configuration update. Each field is set
// only when the request sets it; slugPresent with a nil normalizedSlug clears
// the slug.
type pageUpdate struct {
	normalizedSlug      *string
	slugPresent         bool
	showTitle           *bool
	accessPolicy        *pageaccess.Policy
	encodedAccessPolicy json.RawMessage
}

// change is the Page row update for the fields the request sets.
func (u pageUpdate) change() SettingsChange {
	change := SettingsChange{SetSlug: u.slugPresent, Slug: u.normalizedSlug, ShowTitle: u.showTitle}
	if u.accessPolicy != nil {
		change.AccessPolicy = u.encodedAccessPolicy
	}
	return change
}

// UpdateResult is the Page settings an update leaves behind and whether it
// changed them.
type UpdateResult struct {
	Page    Page
	Changed bool
}

// pageUpdated is the Page settings an update leaves behind.
type pageUpdated struct {
	page          Page
	changedFields []string
}

// UpdatePage changes the Page slug, title visibility or reader audience.
func (s *Service) UpdatePage(ctx context.Context, command UpdatePageCommand) (_ UpdateResult, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.ID))

	update, err := s.preparePageUpdate(ctx, command)
	if err != nil {
		return UpdateResult{}, err
	}
	updated, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[pageUpdated, Store]{
		Access: lockedPage(command.ID, policyv1.Page.Manage),
		Change: func(ctx context.Context, store Store) (pageUpdated, error) {
			return applyPageUpdate(ctx, store, command.ID, update)
		},
		Audit: func(updated pageUpdated) mutation.Audit {
			if len(updated.changedFields) == 0 {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPageConfigurationAuditRecord(metadata, command.ID, updated.changedFields)
			})
		},
		Outbox: func(ctx context.Context, store Store, updated pageUpdated) error {
			if len(updated.changedFields) == 0 {
				return nil
			}
			return store.PublishContentUpdate(ctx, ContentUpdate{PageID: command.ID, Kind: SettingsUpdate, Fields: updatedSettingFields(command)})
		},
	})
	if err != nil {
		if errors.Is(err, ErrSlugTaken) {
			return UpdateResult{}, domainerr.DuplicateSlug("page", update.normalizedSlug).WithCause(err)
		}
		return UpdateResult{}, err
	}
	return UpdateResult{Page: updated.page, Changed: len(updated.changedFields) != 0}, nil
}

// updatedSettingFields names the settings command sets.
func updatedSettingFields(command UpdatePageCommand) []string {
	fields := make([]string, 0, 3)
	if command.Slug != nil {
		fields = append(fields, "slug")
	}
	if command.ShowTitle != nil {
		fields = append(fields, "showTitle")
	}
	if command.AccessPolicy != nil {
		fields = append(fields, "accessPolicy")
	}
	return fields
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
		update.accessPolicy = &policy
		update.encodedAccessPolicy = encoded
	}
	return update, nil
}

// applyPageUpdate writes the Page configuration change and reports the
// fields it changed; none means the Page already had these settings.
func applyPageUpdate(ctx context.Context, store Store, pageID string, update pageUpdate) (pageUpdated, error) {
	lockedPage, err := lockPageSettings(ctx, store, pageID)
	if err != nil {
		return pageUpdated{}, err
	}
	if update.slugPresent && update.normalizedSlug != nil {
		if err := lockAvailablePageSlug(ctx, store, *update.normalizedSlug, pageID); err != nil {
			return pageUpdated{}, err
		}
	}
	if err := validatePageAccessPolicyTags(ctx, store, update.accessPolicy); err != nil {
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
	updated := pageUpdated{page: Page{
		ID: pageID, Slug: cloneOptionalString(previousSlug), ShowTitle: lockedPage.ShowTitle,
		UpdatedAt: lockedPage.UpdatedAt, AccessPolicy: lockedPage.AccessPolicy,
	}}
	updated.changedFields = pageUpdateChangedFields(lockedPage, update)
	if update.accessPolicy != nil && !pageaccess.Equal(currentPolicy, *update.accessPolicy) {
		updated.changedFields = append(updated.changedFields, "access_policy")
	}
	if len(updated.changedFields) == 0 {
		return updated, nil
	}
	// PostgreSQL keeps microseconds; respond with the value later reads return.
	mutationNow := dbtime.Now()
	if err := store.UpdateSettings(ctx, pageID, update.change(), mutationNow); err != nil {
		return pageUpdated{}, err
	}
	page := &updated.page
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
	if err := updatePageMenuTargetAfterSlugChange(ctx, store, pageID, previousSlug, update); err != nil {
		return pageUpdated{}, err
	}
	return updated, nil
}

func pageUpdateChangedFields(page Settings, update pageUpdate) []string {
	fields := []string{}
	if update.slugPresent && !nullableStringEqual(page.Slug, update.normalizedSlug) {
		fields = append(fields, "slug")
	}
	if update.showTitle != nil && *update.showTitle != page.ShowTitle {
		fields = append(fields, "show_title")
	}
	return fields
}

func validatePageAccessPolicyTags(ctx context.Context, store Store, policy *pageaccess.Policy) error {
	if policy == nil || len(policy.UserTagIDs) == 0 {
		return nil
	}
	ids, err := store.LockUserTags(ctx, policy.UserTagIDs)
	if err != nil {
		return err
	}
	if len(ids) != len(policy.UserTagIDs) {
		return domainerr.InvalidField("access_policy.user_tag_ids", "selected user tag does not exist")
	}
	return nil
}

// lockAvailablePageSlug locks the route of slug and rejects a slug another
// Page or resource route holds.
func lockAvailablePageSlug(ctx context.Context, store Store, slug, pageID string) error {
	if err := store.LockRoute(ctx, slug); err != nil {
		return err
	}
	return requireSlugAvailable(ctx, store, slug, pageID)
}

func updatePageMenuTargetAfterSlugChange(
	ctx context.Context,
	store Store,
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
	return store.UpdateMenuTargets(ctx, pageID, derefString(previousSlug), nextTarget)
}
