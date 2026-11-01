package form

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type formUpdatePlan struct {
	row                 formRowUpdate
	normalizedSlug      *string
	nextSourceTitle     *string
	nextSourceSchema    []byte
	needsOg             bool
	refreshAllOgLocales bool
	settingsAuditFields []string
	previousStatus      model.FormStatus
	nextStatus          model.FormStatus
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
	Status                   *model.FormStatus
	AllowedRoles             []string
	ReplaceAllowedRoles      bool
	ClearSlug                bool
	ClearOpensAt             bool
	ClearClosesAt            bool
	ClearMaxSubmissions      bool
}

// UpdateForm applies command to a Form the caller may edit.
func (s *Service) UpdateForm(ctx context.Context, command UpdateFormCommand) (*managev1.Form, error) {
	var form model.Form
	if _, err := mutation.Run(ctx, s.mutations, mutation.Spec[formUpdatePlan]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return lockFormRoot(ctx, tx, command.ID, &form, domainerr.ResourceNotFound("form", command.ID))
		},
		Access: s.freshFormAction(command.ID, formActionEdit),
		Change: func(ctx context.Context, tx *gorm.DB) (formUpdatePlan, error) {
			plan, err := s.prepareFormUpdate(ctx, tx, &form, command)
			if err != nil {
				return formUpdatePlan{}, err
			}
			return plan, s.applyFormUpdate(ctx, tx, &form, &plan)
		},
		Audit: func(plan formUpdatePlan) mutation.Audit {
			return mutation.Audits(
				formSettingsAudit(form.ID, plan.settingsAuditFields),
				formLifecycleAudit(form.ID, plan.previousStatus, plan.nextStatus),
			)
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, plan formUpdatePlan) error {
			if !plan.needsOg {
				return nil
			}
			_, err := s.og.RequestAfterMutation(ctx, tx, form.ID, form.SourceLocale, plan.refreshAllOgLocales, "form_updated")
			return err
		},
	}); err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).First(&form, "id = ?", command.ID).Error; err != nil {
		return nil, err
	}
	return s.toProtoForm(ctx, &form)
}

func (s *Service) prepareFormUpdate(
	ctx context.Context,
	db *gorm.DB,
	form *model.Form,
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
	normalizedSlug, err := s.normalizeFormUpdateSlug(ctx, db, form.ID, r)
	if err != nil {
		return formUpdatePlan{}, err
	}
	currentSourceState, err := LoadFormCanonicalSourceDocumentState(ctx, db, form.ID, form.SourceLocale)
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
		row.next.AllowedRoles = pq.StringArray(allowedRoles)
		row.set(formColumnAllowedRoles)
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

// formRowUpdate is the Form row an update leads to and the settings columns
// the request sets on it; only those columns and updated_at are written.
type formRowUpdate struct {
	next      model.Form
	columns   []formColumn
	updatedAt time.Time
}

type formColumn string

const (
	formColumnSlug                     formColumn = "slug"
	formColumnIsPublic                 formColumn = "is_public"
	formColumnRequireAuth              formColumn = "require_auth"
	formColumnAllowDuplicateSubmission formColumn = "allow_duplicate_submission"
	formColumnAccessPassword           formColumn = "access_password"
	formColumnMaxSubmissions           formColumn = "max_submissions"
	formColumnOpensAt                  formColumn = "opens_at"
	formColumnClosesAt                 formColumn = "closes_at"
	formColumnStatus                   formColumn = "status"
	formColumnAllowedRoles             formColumn = "allowed_roles"
)

func (u *formRowUpdate) set(column formColumn) {
	u.columns = append(u.columns, column)
}

// values is the column map for the row write.
func (u formRowUpdate) values() structured.Fields {
	values := structured.Fields{"updated_at": u.updatedAt}
	next := u.next
	for _, column := range u.columns {
		var value any
		switch column {
		case formColumnSlug:
			value = next.Slug
		case formColumnIsPublic:
			value = next.IsPublic
		case formColumnRequireAuth:
			value = next.RequireAuth
		case formColumnAllowDuplicateSubmission:
			value = next.AllowDuplicateSubmission
		case formColumnAccessPassword:
			value = next.AccessPassword
		case formColumnMaxSubmissions:
			value = next.MaxSubmissions
		case formColumnOpensAt:
			value = next.OpensAt
		case formColumnClosesAt:
			value = next.ClosesAt
		case formColumnStatus:
			value = string(next.Status)
		case formColumnAllowedRoles:
			value = next.AllowedRoles
		}
		values[string(column)] = value
	}
	return values
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

func (s *Service) normalizeFormUpdateSlug(
	ctx context.Context,
	db *gorm.DB,
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
	if err := ensureSlugAvailable(ctx, db, slug, formID); err != nil {
		return nil, err
	}
	if err := s.routes.EnsureAvailable(ctx, db, slug, formID); err != nil {
		return nil, err
	}
	return &slug, nil
}

func applyFormSlugUpdate(row *formRowUpdate, r UpdateFormCommand, slug *string) {
	switch {
	case r.ClearSlug || r.Slug != nil && strings.TrimSpace(*r.Slug) == "":
		row.next.Slug = nil
		row.set(formColumnSlug)
	case slug != nil:
		row.next.Slug = slug
		row.set(formColumnSlug)
	}
}

func applyFormAccessUpdates(row *formRowUpdate, r UpdateFormCommand) {
	if r.IsPublic != nil {
		row.next.IsPublic = *r.IsPublic
		row.set(formColumnIsPublic)
	}
	if r.RequireAuth != nil {
		row.next.RequireAuth = r.RequireAuth
		row.set(formColumnRequireAuth)
	}
	if r.AllowDuplicateSubmission != nil {
		row.next.AllowDuplicateSubmission = r.AllowDuplicateSubmission
		row.set(formColumnAllowDuplicateSubmission)
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
		row.set(formColumnAccessPassword)
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
	row.set(formColumnAccessPassword)
	return true, nil
}

func applyFormLimitAndScheduleUpdates(row *formRowUpdate, r UpdateFormCommand) {
	switch {
	case r.ClearMaxSubmissions:
		row.next.MaxSubmissions = nil
		row.set(formColumnMaxSubmissions)
	case r.MaxSubmissions != nil:
		row.next.MaxSubmissions = r.MaxSubmissions
		row.set(formColumnMaxSubmissions)
	}
	if update, ok := formTimestampUpdate(r.ClearOpensAt, r.OpensAt); ok {
		row.next.OpensAt = update
		row.set(formColumnOpensAt)
	}
	if update, ok := formTimestampUpdate(r.ClearClosesAt, r.ClosesAt); ok {
		row.next.ClosesAt = update
		row.set(formColumnClosesAt)
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
	current *model.Form,
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
	case model.FormStatusDraft, model.FormStatusPublished:
	default:
		return domainerr.InvalidField("status", "must be draft or published")
	}
	row.next.Status = status
	row.set(formColumnStatus)
	if current.Status != model.FormStatusPublished && row.next.Status == model.FormStatusPublished {
		plan.needsOg = true
		plan.refreshAllOgLocales = true
	}
	return nil
}

// applyFormUpdate writes plan's row update and source locale document. A
// changed source title marks the plan for an OG request.
func (s *Service) applyFormUpdate(
	ctx context.Context,
	tx *gorm.DB,
	form *model.Form,
	plan *formUpdatePlan,
) error {
	if plan.normalizedSlug != nil {
		if err := s.routes.EnsureAvailableLocked(ctx, tx, *plan.normalizedSlug, form.ID); err != nil {
			return err
		}
	}
	if err := tx.WithContext(ctx).Model(form).Updates(plan.row.values()).Error; err != nil {
		return err
	}
	if plan.nextSourceTitle == nil && plan.nextSourceSchema == nil {
		return nil
	}
	documentID, err := formContentDocumentID(form.ContentDocumentID)
	if err != nil {
		return err
	}
	documentRevision, err := contentblock.CurrentRevision(ctx, tx, documentID)
	if err != nil {
		return err
	}
	advance, err := advanceFormSourceLocale(ctx, tx, s.contentBlocks, form.ContentDocumentID, documentRevision, form.SourceLocale,
		func(ctx context.Context, tx *gorm.DB) (bool, error) {
			return true, saveFormSourceLocaleDocumentState(ctx, tx, form.ID, form.SourceLocale, TranslationDocumentSaveInput{
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

func formSettingsAuditFields(current, next *model.Form, passwordChanged bool) []string {
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
	if !slices.Equal([]string(current.AllowedRoles), []string(next.AllowedRoles)) {
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
