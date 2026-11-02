package form

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pathslug"
)

type formUpdatePlan struct {
	row                 formRowUpdate
	normalizedSlug      *string
	nextSourceTitle     *string
	nextSourceSchema    []byte
	needsOg             bool
	refreshAllOgLocales bool
	settingsAuditFields []string
	previousStatus      Status
	nextStatus          Status
}

// UpdateFormCommand changes a Form's source title and schema and its
// settings. A Clear field clears its setting; setting and clearing the same
// one is rejected. Status is the requested stored status; a value the API
// does not know is empty.
type UpdateFormCommand struct {
	ID                       string
	Title                    *string
	Slug                     *string
	Schema                   []byte
	IsPublic                 *bool
	RequireAuth              *bool
	AllowDuplicateSubmission *bool
	Password                 *string
	MaxSubmissions           *int32
	OpensAt                  *time.Time
	ClosesAt                 *time.Time
	Status                   *Status
	AllowedRoles             []string
	ReplaceAllowedRoles      bool
	ClearSlug                bool
	ClearOpensAt             bool
	ClearClosesAt            bool
	ClearMaxSubmissions      bool
}

// UpdateForm applies command to a Form the caller may edit.
func (s *Service) UpdateForm(ctx context.Context, command UpdateFormCommand) (View, error) {
	var form Form
	if _, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[formUpdatePlan, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			form, err = store.LockForm(ctx, command.ID)
			return formNotFound(err, domainerr.ResourceNotFound("form", command.ID))
		},
		Access: s.freshFormAction(command.ID, formActionEdit),
		Change: func(ctx context.Context, store Store) (formUpdatePlan, error) {
			plan, err := s.prepareFormUpdate(ctx, store, &form, command)
			if err != nil {
				return formUpdatePlan{}, err
			}
			return plan, applyFormUpdate(ctx, store, &form, &plan)
		},
		Audit: func(plan formUpdatePlan) mutation.Audit {
			return mutation.Audits(
				formSettingsAudit(form.ID, plan.settingsAuditFields),
				formLifecycleAudit(form.ID, plan.previousStatus, plan.nextStatus),
			)
		},
		Outbox: func(ctx context.Context, store Store, plan formUpdatePlan) error {
			if !plan.needsOg {
				return nil
			}
			_, err := store.RequestOG(ctx, form.ID, form.SourceLocale, plan.refreshAllOgLocales, "form_updated")
			return err
		},
	}); err != nil {
		return View{}, err
	}
	var updated Form
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		updated, err = store.FindForm(ctx, ByID, command.ID)
		return err
	}); err != nil {
		return View{}, err
	}
	return s.view(ctx, updated)
}

func (s *Service) prepareFormUpdate(
	ctx context.Context,
	store Store,
	form *Form,
	r UpdateFormCommand,
) (formUpdatePlan, error) {
	if err := validateFormUpdateFieldConflicts(r); err != nil {
		return formUpdatePlan{}, err
	}
	if r.Schema != nil {
		if err := validateCanonicalFormSchema(r.Schema); err != nil {
			return formUpdatePlan{}, domainerr.New(domainerr.Invalid, "", err.Error())
		}
	}
	normalizedSlug, err := normalizeFormUpdateSlug(ctx, store, form.ID, r)
	if err != nil {
		return formUpdatePlan{}, err
	}
	currentSourceState, err := LoadFormCanonicalSourceDocumentState(ctx, store, form.ID, form.SourceLocale)
	if err != nil {
		return formUpdatePlan{}, err
	}
	plan := formUpdatePlan{
		row:            formRowUpdate{next: *form, updatedAt: time.Now()},
		normalizedSlug: normalizedSlug,
	}
	currentTitle := resolveFormTitle(currentSourceState.Title)
	if r.Title != nil {
		if strings.TrimSpace(*r.Title) == "" {
			return formUpdatePlan{}, domainerr.Required("title")
		}
		if currentTitle != *r.Title {
			plan.nextSourceTitle = r.Title
			plan.needsOg = true
		}
	}
	row := &plan.row
	applyFormSlugUpdate(row, r, normalizedSlug)
	if r.Schema != nil {
		if !bytes.Equal(currentSourceState.ContentJSON, r.Schema) {
			plan.nextSourceSchema = r.Schema
		}
	}
	applyFormAccessUpdates(row, r)
	passwordChanged, err := s.applyFormPasswordUpdate(row, r.Password)
	if err != nil {
		return formUpdatePlan{}, err
	}
	applyFormLimitAndScheduleUpdates(row, r)
	if err := applyFormStatusUpdate(row, form, r, &plan); err != nil {
		return formUpdatePlan{}, err
	}
	if r.ReplaceAllowedRoles || len(r.AllowedRoles) > 0 {
		allowedRoles, err := normalizeUserRoleList("allowed_roles", r.AllowedRoles)
		if err != nil {
			return formUpdatePlan{}, err
		}
		row.next.AllowedRoles = allowedRoles
		row.set(SettingAllowedRoles)
	}
	if err := validateFormSettings(&row.next); err != nil {
		return formUpdatePlan{}, err
	}
	plan.settingsAuditFields = formSettingsAuditFields(form, &row.next, passwordChanged)
	if form.Status != row.next.Status {
		plan.previousStatus = form.Status
		plan.nextStatus = row.next.Status
	}
	return plan, nil
}

// formRowUpdate is the Form row an update leads to and the settings the
// request sets on it; only those settings and updated_at are written.
type formRowUpdate struct {
	next      Form
	settings  []Setting
	updatedAt time.Time
}

func (u *formRowUpdate) set(setting Setting) {
	u.settings = append(u.settings, setting)
}

func validateFormUpdateFieldConflicts(r UpdateFormCommand) error {
	for _, conflict := range []struct {
		clear bool
		set   bool
		field string
	}{
		{clear: r.ClearSlug, set: r.Slug != nil, field: "slug"},
		{clear: r.ClearOpensAt, set: r.OpensAt != nil, field: "opens_at"},
		{clear: r.ClearClosesAt, set: r.ClosesAt != nil, field: "closes_at"},
		{clear: r.ClearMaxSubmissions, set: r.MaxSubmissions != nil, field: "max_submissions"},
	} {
		if conflict.clear && conflict.set {
			return domainerr.InvalidField(conflict.field, "cannot set and clear "+conflict.field+" in the same request")
		}
	}
	return nil
}

func normalizeFormUpdateSlug(
	ctx context.Context,
	store Store,
	formID string,
	r UpdateFormCommand,
) (*string, error) {
	if r.Slug == nil || strings.TrimSpace(*r.Slug) == "" {
		return nil, nil
	}
	slug := strings.TrimSpace(*r.Slug)
	if err := pathslug.Validate(slug); err != nil {
		return nil, err
	}
	if err := ensureSlugAvailable(ctx, store, slug, formID); err != nil {
		return nil, err
	}
	return &slug, nil
}

func applyFormSlugUpdate(row *formRowUpdate, r UpdateFormCommand, slug *string) {
	switch {
	case r.ClearSlug || r.Slug != nil && strings.TrimSpace(*r.Slug) == "":
		row.next.Slug = nil
		row.set(SettingSlug)
	case slug != nil:
		row.next.Slug = slug
		row.set(SettingSlug)
	}
}

func applyFormAccessUpdates(row *formRowUpdate, r UpdateFormCommand) {
	if r.IsPublic != nil {
		row.next.IsPublic = *r.IsPublic
		row.set(SettingIsPublic)
	}
	if r.RequireAuth != nil {
		row.next.RequireAuth = r.RequireAuth
		row.set(SettingRequireAuth)
	}
	if r.AllowDuplicateSubmission != nil {
		row.next.AllowDuplicateSubmission = r.AllowDuplicateSubmission
		row.set(SettingAllowDuplicateSubmission)
	}
}

func (s *Service) applyFormPasswordUpdate(row *formRowUpdate, password *string) (bool, error) {
	if password == nil {
		return false, nil
	}
	currentHash := row.next.AccessPassword
	if *password == "" {
		if currentHash == nil {
			return false, nil
		}
		row.next.AccessPassword = nil
		row.set(SettingAccessPassword)
		return true, nil
	}
	if currentHash != nil {
		matches, err := s.password.Verify(*password, *currentHash)
		if err != nil {
			return false, err
		}
		if matches {
			return false, nil
		}
	}
	hash, err := s.password.Hash(*password)
	if err != nil {
		return false, err
	}
	row.next.AccessPassword = &hash
	row.set(SettingAccessPassword)
	return true, nil
}

func applyFormLimitAndScheduleUpdates(row *formRowUpdate, r UpdateFormCommand) {
	switch {
	case r.ClearMaxSubmissions:
		row.next.MaxSubmissions = nil
		row.set(SettingMaxSubmissions)
	case r.MaxSubmissions != nil:
		row.next.MaxSubmissions = r.MaxSubmissions
		row.set(SettingMaxSubmissions)
	}
	if update, ok := formTimestampUpdate(r.ClearOpensAt, r.OpensAt); ok {
		row.next.OpensAt = update
		row.set(SettingOpensAt)
	}
	if update, ok := formTimestampUpdate(r.ClearClosesAt, r.ClosesAt); ok {
		row.next.ClosesAt = update
		row.set(SettingClosesAt)
	}
}

// formTimestampUpdate returns the requested value of an optional timestamp
// and whether the request sets or clears it.
func formTimestampUpdate(clear bool, value *time.Time) (*time.Time, bool) {
	if clear {
		return nil, true
	}
	return value, value != nil
}

func applyFormStatusUpdate(
	row *formRowUpdate,
	current *Form,
	r UpdateFormCommand,
	plan *formUpdatePlan,
) error {
	if r.Status == nil {
		return nil
	}
	status := *r.Status
	switch status {
	case current.Status:
		return nil
	case StatusDraft, StatusPublished:
	default:
		return domainerr.InvalidField("status", "must be draft or published")
	}
	row.next.Status = status
	row.set(SettingStatus)
	if current.Status != StatusPublished && row.next.Status == StatusPublished {
		plan.needsOg = true
		plan.refreshAllOgLocales = true
	}
	return nil
}

// applyFormUpdate writes plan's row update and source locale document. A
// changed source title marks the plan for an OG request.
func applyFormUpdate(
	ctx context.Context,
	store Store,
	form *Form,
	plan *formUpdatePlan,
) error {
	if plan.normalizedSlug != nil {
		if err := store.LockRoute(ctx, *plan.normalizedSlug); err != nil {
			return err
		}
	}
	if err := store.UpdateSettings(ctx, plan.row.next, plan.row.settings, plan.row.updatedAt); err != nil {
		return err
	}
	if plan.nextSourceTitle == nil && plan.nextSourceSchema == nil {
		return nil
	}
	documentID, err := formContentDocumentID(form.ContentDocumentID)
	if err != nil {
		return err
	}
	documentRevision, err := store.DocumentRevision(ctx, documentID)
	if err != nil {
		return err
	}
	advance, err := advanceFormSourceLocale(ctx, store, form.ContentDocumentID, documentRevision, form.SourceLocale,
		func(ctx context.Context, store Store) (bool, error) {
			return true, saveFormSourceLocaleDocumentState(ctx, store, form.ID, form.SourceLocale, TranslationDocumentSaveInput{
				Title: plan.nextSourceTitle, ContentJSON: plan.nextSourceSchema, Now: time.Now().UTC(),
			})
		})
	if err != nil {
		return err
	}
	if advance.Changed {
		plan.needsOg = plan.needsOg || plan.nextSourceTitle != nil
	}
	return nil
}

func formSettingsAuditFields(current, next *Form, passwordChanged bool) []string {
	fields := make([]string, 0, 8)
	if !sameOptionalString(current.Slug, next.Slug) {
		fields = append(fields, "slug")
	}
	if current.IsPublic != next.IsPublic {
		fields = append(fields, "direct_public")
	}
	if !sameOptionalBool(current.RequireAuth, next.RequireAuth) {
		fields = append(fields, "auth_required")
	}
	if !sameOptionalBool(current.AllowDuplicateSubmission, next.AllowDuplicateSubmission) {
		fields = append(fields, "duplicate_policy")
	}
	if !sameOptionalInt32(current.MaxSubmissions, next.MaxSubmissions) {
		fields = append(fields, "limit")
	}
	if !sameOptionalTime(current.OpensAt, next.OpensAt) || !sameOptionalTime(current.ClosesAt, next.ClosesAt) {
		fields = append(fields, "access_period")
	}
	if !slices.Equal(current.AllowedRoles, next.AllowedRoles) {
		fields = append(fields, "required_role")
	}
	if passwordChanged {
		fields = append(fields, "password")
	}
	return fields
}

func sameOptionalBool(a, b *bool) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
func sameOptionalString(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
func sameOptionalInt32(a, b *int32) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
func sameOptionalTime(a, b *time.Time) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && a.Equal(*b))
}
