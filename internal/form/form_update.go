package form

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type formUpdatePlan struct {
	row                 formRowUpdate
	normalizedSlug      *string
	nextSourceTitle     *string
	nextSourceSchema    []byte
	needsOg             bool
	refreshAllOgLocales bool
	settingsAuditFields []string
	lifecycleChanged    bool
	previousStatus      model.FormStatus
	nextStatus          model.FormStatus
}

func (s *FormService) UpdateForm(
	ctx context.Context,
	req *connect.Request[managev1.UpdateFormRequest],
) (*connect.Response[managev1.Form], error) {
	var form model.Form
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			First(&form, "id = ?", req.Msg.Id).Error; err != nil {
			return err
		}
		if err := s.requireFreshFormAction(ctx, tx, form.ID, formActionEdit); err != nil {
			return err
		}
		plan, err := s.prepareFormUpdate(ctx, tx, &form, req.Msg)
		if err != nil {
			return err
		}
		return s.applyFormUpdate(ctx, tx, &form, plan)
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("form", req.Msg.Id)
		}
		return nil, errs.Wrap(err)
	}
	if err := s.db.WithContext(ctx).First(&form, "id = ?", req.Msg.Id).Error; err != nil {
		return nil, errs.Internal(err)
	}
	protoForm, err := s.toProtoForm(ctx, &form)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(protoForm), nil
}

func (s *FormService) prepareFormUpdate(
	ctx context.Context,
	db *gorm.DB,
	form *model.Form,
	r *managev1.UpdateFormRequest,
) (formUpdatePlan, error) {
	if err := validateFormUpdateFieldConflicts(r); err != nil {
		return formUpdatePlan{}, err
	}
	if r.Schema != nil {
		if err := validateCanonicalFormSchema(r.Schema); err != nil {
			return formUpdatePlan{}, errs.InvalidArgumentMsg(err.Error())
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
			return formUpdatePlan{}, errs.Required("title")
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
		plan.lifecycleChanged = true
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

func validateFormUpdateFieldConflicts(r *managev1.UpdateFormRequest) error {
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
			return errs.InvalidArgument(conflict.field, "cannot set and clear "+conflict.field+" in the same request")
		}
	}
	return nil
}

func (s *FormService) normalizeFormUpdateSlug(
	ctx context.Context,
	db *gorm.DB,
	formID string,
	r *managev1.UpdateFormRequest,
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

func applyFormSlugUpdate(row *formRowUpdate, r *managev1.UpdateFormRequest, slug *string) {
	switch {
	case r.ClearSlug || r.Slug != nil && strings.TrimSpace(*r.Slug) == "":
		row.next.Slug = nil
		row.set(formColumnSlug)
	case slug != nil:
		row.next.Slug = slug
		row.set(formColumnSlug)
	}
}

func applyFormAccessUpdates(row *formRowUpdate, r *managev1.UpdateFormRequest) {
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

func (s *FormService) applyFormPasswordUpdate(row *formRowUpdate, password *string) (bool, error) {
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
			return false, errs.Internal(err)
		}
		if matches {
			return false, nil
		}
	}
	hash, err := s.password.Hash(*password)
	if err != nil {
		return false, errs.Internal(err)
	}
	row.next.AccessPassword = &hash
	row.set(formColumnAccessPassword)
	return true, nil
}

func applyFormLimitAndScheduleUpdates(row *formRowUpdate, r *managev1.UpdateFormRequest) {
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
func formTimestampUpdate(clear bool, value *timestamppb.Timestamp) (*time.Time, bool) {
	if clear {
		return nil, true
	}
	if value == nil {
		return nil, false
	}
	timestamp := value.AsTime()
	return &timestamp, true
}

func applyFormStatusUpdate(
	row *formRowUpdate,
	current *model.Form,
	r *managev1.UpdateFormRequest,
	plan *formUpdatePlan,
) error {
	if r.Status == nil {
		return nil
	}
	if current.Status == model.FormStatus(r.Status.String()) {
		return nil
	}
	if *r.Status != managev1.FormStatus_FORM_STATUS_DRAFT && *r.Status != managev1.FormStatus_FORM_STATUS_PUBLISHED {
		return errs.InvalidArgument("status", "must be draft or published")
	}
	row.next.Status = model.FormStatus(r.Status.String())
	row.set(formColumnStatus)
	published := model.FormStatus(managev1.FormStatus_FORM_STATUS_PUBLISHED.String())
	if current.Status != published && row.next.Status == published {
		plan.needsOg = true
		plan.refreshAllOgLocales = true
	}
	return nil
}

func (s *FormService) applyFormUpdate(
	ctx context.Context,
	tx *gorm.DB,
	form *model.Form,
	plan formUpdatePlan,
) error {
	if plan.normalizedSlug != nil {
		if err := s.routes.EnsureAvailableLocked(ctx, tx, *plan.normalizedSlug, form.ID); err != nil {
			return err
		}
	}
	if err := tx.WithContext(ctx).Model(form).Updates(plan.row.values()).Error; err != nil {
		return err
	}
	sourceLocale := form.SourceLocale
	if plan.nextSourceTitle != nil || plan.nextSourceSchema != nil {
		documentID, err := uuid.Parse(form.ContentDocumentID)
		if err != nil || documentID == uuid.Nil {
			return errs.WithCause(errs.FailedPrecondition("Form Content Document is not initialized"), failurecause.ServerFault(err))
		}
		var documentRevision uuid.UUID
		if err := tx.WithContext(ctx).Table("content_document").Select("revision").Where("id = ?", documentID).Take(&documentRevision).Error; err != nil {
			return errs.Internal(err)
		}
		advance, err := s.contentBlocks.AdvanceRevision(
			ctx,
			tx,
			contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: documentRevision},
			func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
				return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
			},
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				if err := saveFormSourceLocaleDocumentState(ctx, tx, form.ID, sourceLocale, TranslationDocumentSaveInput{
					Title: plan.nextSourceTitle, ContentJSON: plan.nextSourceSchema, Now: time.Now().UTC(),
				}); err != nil {
					return contentblock.MetadataEffect{}, err
				}
				return contentblock.MetadataEffect{Changed: true, AffectsTranslationSource: true, ChangedLocales: []string{sourceLocale}}, nil
			},
		)
		if err != nil {
			return err
		}
		if advance.Changed {
			plan.needsOg = plan.needsOg || plan.nextSourceTitle != nil
		}
	}
	if err := s.appendFormSettingsAudit(ctx, tx, form.ID, plan.settingsAuditFields); err != nil {
		return err
	}
	if plan.lifecycleChanged {
		if err := s.appendFormLifecycleAudit(
			ctx,
			tx,
			form.ID,
			formAuditState(plan.previousStatus),
			formAuditState(plan.nextStatus),
		); err != nil {
			return err
		}
	}
	if !plan.needsOg {
		return nil
	}
	_, err := s.og.RequestAfterMutation(ctx, tx, form.ID, sourceLocale, plan.refreshAllOgLocales, "form_updated")
	return err
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

func formAuditState(status model.FormStatus) sharedtelemetry.AuditState {
	if status == model.FormStatus(managev1.FormStatus_FORM_STATUS_PUBLISHED.String()) {
		return sharedtelemetry.AuditStatePublished
	}
	return sharedtelemetry.AuditStateDraft
}
