package form

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
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
	fields              structured.Fields
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
			Clauses(clause.Locking{Strength: "UPDATE"}).
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
		if err == gorm.ErrRecordNotFound {
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
		fields:         structured.Fields{"updated_at": time.Now()},
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
	nextForm := *form
	applyFormSlugUpdate(plan.fields, &nextForm, r, normalizedSlug)
	if r.Schema != nil {
		if !bytes.Equal(currentSourceState.ContentJSON, r.Schema) {
			plan.nextSourceSchema = r.Schema
		}
	}
	applyFormAccessUpdates(plan.fields, &nextForm, r)
	passwordChanged, err := s.applyFormPasswordUpdate(plan.fields, form.AccessPassword, r.Password)
	if err != nil {
		return formUpdatePlan{}, err
	}
	applyFormLimitAndScheduleUpdates(plan.fields, &nextForm, r)
	if err := applyFormStatusUpdate(plan.fields, form, &nextForm, r, &plan); err != nil {
		return formUpdatePlan{}, err
	}
	if r.ReplaceAllowedRoles || len(r.AllowedRoles) > 0 {
		allowedRoles, err := normalizeUserRoleList("allowed_roles", r.AllowedRoles)
		if err != nil {
			return formUpdatePlan{}, err
		}
		plan.fields["allowed_roles"] = pq.StringArray(allowedRoles)
		nextForm.AllowedRoles = pq.StringArray(allowedRoles)
	}
	if err := validateFormSettings(&nextForm); err != nil {
		return formUpdatePlan{}, err
	}
	plan.settingsAuditFields = formSettingsAuditFields(form, &nextForm, passwordChanged)
	if form.Status != nextForm.Status {
		plan.lifecycleChanged = true
		plan.previousStatus = form.Status
		plan.nextStatus = nextForm.Status
	}
	return plan, nil
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
	if err := validateSlugWithoutSlash(slug); err != nil {
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

func applyFormSlugUpdate(fields structured.Fields, form *model.Form, r *managev1.UpdateFormRequest, slug *string) {
	switch {
	case r.ClearSlug || r.Slug != nil && strings.TrimSpace(*r.Slug) == "":
		fields["slug"] = nil
		form.Slug = nil
	case slug != nil:
		fields["slug"] = *slug
		form.Slug = slug
	}
}

func applyFormAccessUpdates(fields structured.Fields, form *model.Form, r *managev1.UpdateFormRequest) {
	if r.IsPublic != nil {
		fields["is_public"] = *r.IsPublic
		form.IsPublic = *r.IsPublic
	}
	if r.RequireAuth != nil {
		fields["require_auth"] = *r.RequireAuth
		form.RequireAuth = r.RequireAuth
	}
	if r.AllowDuplicateSubmission != nil {
		fields["allow_duplicate_submission"] = *r.AllowDuplicateSubmission
		form.AllowDuplicateSubmission = r.AllowDuplicateSubmission
	}
}

func (s *FormService) applyFormPasswordUpdate(
	fields structured.Fields,
	currentHash *string,
	password *string,
) (bool, error) {
	if password == nil {
		return false, nil
	}
	if *password == "" {
		if currentHash == nil {
			return false, nil
		}
		fields["access_password"] = nil
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
	fields["access_password"] = hash
	return true, nil
}

func applyFormLimitAndScheduleUpdates(fields structured.Fields, form *model.Form, r *managev1.UpdateFormRequest) {
	switch {
	case r.ClearMaxSubmissions:
		fields["max_submissions"] = nil
		form.MaxSubmissions = nil
	case r.MaxSubmissions != nil:
		fields["max_submissions"] = *r.MaxSubmissions
		form.MaxSubmissions = r.MaxSubmissions
	}
	applyFormTimestampUpdate(
		fields, "opens_at", r.ClearOpensAt, r.OpensAt,
		func(value *time.Time) { form.OpensAt = value },
	)
	applyFormTimestampUpdate(
		fields, "closes_at", r.ClearClosesAt, r.ClosesAt,
		func(value *time.Time) { form.ClosesAt = value },
	)
}

func applyFormTimestampUpdate(
	fields structured.Fields,
	column string,
	clear bool,
	value *timestamppb.Timestamp,
	assign func(*time.Time),
) {
	if clear {
		fields[column] = nil
		assign(nil)
		return
	}
	if value != nil {
		timestamp := value.AsTime()
		fields[column] = timestamp
		assign(&timestamp)
	}
}

func applyFormStatusUpdate(
	fields structured.Fields,
	current *model.Form,
	next *model.Form,
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
	fields["status"] = r.Status.String()
	next.Status = model.FormStatus(r.Status.String())
	published := model.FormStatus(managev1.FormStatus_FORM_STATUS_PUBLISHED.String())
	if current.Status != published && next.Status == published {
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
	if err := tx.WithContext(ctx).Model(form).Updates(plan.fields).Error; err != nil {
		return err
	}
	sourceLocale := form.SourceLocale
	if plan.nextSourceTitle != nil || plan.nextSourceSchema != nil {
		documentID, err := uuid.Parse(form.ContentDocumentID)
		if err != nil || documentID == uuid.Nil {
			return errs.FailedPrecondition("Form Content Document is not initialized")
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
