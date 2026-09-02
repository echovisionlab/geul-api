package public

import (
	"context"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// FormService implements the public FormService
func (s *FormService) validateShareToken(
	ctx context.Context,
	form *model.Form,
	shareToken *string,
	sharePassword *string,
	allowedTypes ...managev1.ShareLinkEntityType,
) formShareTokenState {
	if shareToken == nil || *shareToken == "" {
		return formShareTokenState{}
	}

	var shareLink model.ShareLink
	if err := s.db.WithContext(ctx).First(&shareLink, "token = ?", *shareToken).Error; err != nil {
		return formShareTokenState{}
	}

	// Check expiry
	if shareLink.ExpiresAt == nil || !shareLink.ExpiresAt.After(time.Now()) {
		return formShareTokenState{}
	}
	if shareLink.PasswordHash != nil {
		if sharePassword == nil || strings.TrimSpace(*sharePassword) == "" {
			return formShareTokenState{}
		}
		matched, err := s.password.Verify(*sharePassword, *shareLink.PasswordHash)
		if err != nil || !matched {
			return formShareTokenState{}
		}
	}

	// Check entity ID
	if shareLink.EntityID != form.ID {
		return formShareTokenState{}
	}

	shareTypeValue, ok := managev1.ShareLinkEntityType_value[shareLink.EntityType]
	if !ok {
		return formShareTokenState{}
	}

	shareType := managev1.ShareLinkEntityType(shareTypeValue)
	allowed := slices.Contains(allowedTypes, shareType)
	if !allowed {
		return formShareTokenState{}
	}

	return formShareTokenState{valid: true}
}

func (s *FormService) findFormBySlugOrID(ctx context.Context, slugOrID string) (*model.Form, error) {
	var form model.Form
	var err error

	if formdomain.IsValidUUID(slugOrID) {
		err = s.db.WithContext(ctx).First(&form, "id = ?", slugOrID).Error
	} else {
		err = s.db.WithContext(ctx).First(&form, "slug = ?", slugOrID).Error
	}

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("form not found")
		}
		return nil, errs.Internal(err)
	}

	return &form, nil
}

type formAccessOptions struct {
	context                  openv1.FormAccessContext
	hasValidPreviewToken     bool
	bypassAuth               bool
	bypassRoles              bool
	draftAsNotFound          bool
	enforcePassword          bool
	password                 *string
	checkSubmissionLimit     bool
	checkDuplicateSubmission bool
}

// enforceFormAccess applies the unified public form policy across Get/Submit/VerifyPassword.
func (s *FormService) enforceFormAccess(ctx context.Context, form *model.Form, opts formAccessOptions) error {
	reason, err := s.evaluateFormAccess(ctx, form, opts)
	if err != nil {
		return err
	}

	return mapFormAccessReasonToError(reason, opts.draftAsNotFound)
}

func (s *FormService) evaluateFormAccess(
	ctx context.Context,
	form *model.Form,
	opts formAccessOptions,
) (openv1.FormAccessReason, error) {
	user := auth.GetUser(ctx)

	isPublished := form.Status == model.FormStatus(managev1.FormStatus_FORM_STATUS_PUBLISHED.String())
	if !isPublished && !opts.hasValidPreviewToken {
		return openv1.FormAccessReason_FORM_ACCESS_REASON_FORM_NOT_PUBLISHED, nil
	}

	// Public URL access is allowed only when explicitly enabled,
	// unless this is preview-token access.
	if normalizeFormAccessContext(opts.context) == openv1.FormAccessContext_FORM_ACCESS_CONTEXT_URL &&
		!opts.hasValidPreviewToken &&
		!form.IsPublic {
		return openv1.FormAccessReason_FORM_ACCESS_REASON_NOT_PUBLIC, nil
	}

	requireAuthActive := form.RequireAuth != nil && *form.RequireAuth && !opts.bypassAuth
	allowedRolesActive := len(form.AllowedRoles) > 0 && !opts.bypassRoles

	if requireAuthActive && (user == nil || !user.Authenticated || user.Banned) {
		return openv1.FormAccessReason_FORM_ACCESS_REASON_AUTH_REQUIRED, nil
	}
	if reason, handled, err := s.evaluateDuplicateFormSubmission(
		ctx, form, opts, user, requireAuthActive || allowedRolesActive,
	); handled || err != nil {
		return reason, err
	}
	if reason, err := s.evaluateFormRoleAccess(
		ctx, user, form.AllowedRoles, allowedRolesActive,
	); err != nil || reason != openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED {
		if err != nil {
			return openv1.FormAccessReason_FORM_ACCESS_REASON_UNSPECIFIED, errs.Internal(err)
		}
		return reason, nil
	}
	if reason := evaluateFormScheduleAccess(
		form, opts.hasValidPreviewToken, time.Now(),
	); reason != openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED {
		return reason, nil
	}
	if reason, err := s.evaluateFormSubmissionLimit(
		ctx, form, opts.checkSubmissionLimit,
	); err != nil || reason != openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED {
		return reason, err
	}
	if formPasswordRequired(s, form, opts) {
		return openv1.FormAccessReason_FORM_ACCESS_REASON_PASSWORD_REQUIRED, nil
	}

	return openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED, nil
}

func (s *FormService) evaluateDuplicateFormSubmission(
	ctx context.Context,
	form *model.Form,
	opts formAccessOptions,
	user *auth.UserInfo,
	requiresAuthenticatedIdentity bool,
) (openv1.FormAccessReason, bool, error) {
	shouldCheck := opts.checkDuplicateSubmission && requiresAuthenticatedIdentity && !opts.hasValidPreviewToken &&
		user != nil && form.AllowDuplicateSubmission != nil && !*form.AllowDuplicateSubmission
	if !shouldCheck {
		return openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED, false, nil
	}
	exists, err := s.hasMemberSubmission(ctx, form.ID, user.MemberID.String())
	if err != nil {
		return openv1.FormAccessReason_FORM_ACCESS_REASON_UNSPECIFIED, true, errs.Internal(err)
	}
	if exists {
		return openv1.FormAccessReason_FORM_ACCESS_REASON_ALREADY_SUBMITTED, true, nil
	}
	return openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED, false, nil
}

func (s *FormService) evaluateFormRoleAccess(
	ctx context.Context,
	user *auth.UserInfo,
	allowedRoles []string,
	active bool,
) (openv1.FormAccessReason, error) {
	if !active {
		return openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED, nil
	}
	if user == nil {
		return openv1.FormAccessReason_FORM_ACCESS_REASON_AUTH_REQUIRED, nil
	}
	if !user.Authenticated || user.Banned || strings.TrimSpace(user.IdentityID.String()) == "" {
		return openv1.FormAccessReason_FORM_ACCESS_REASON_AUTH_REQUIRED, nil
	}
	for _, role := range allowedRoles {
		can, ok, err := formRoleCan(role)
		if err != nil {
			return openv1.FormAccessReason_FORM_ACCESS_REASON_UNSPECIFIED, err
		}
		if !ok {
			continue
		}
		decision, err := auth.AuthorizationDecision(ctx, can)
		if err != nil {
			return openv1.FormAccessReason_FORM_ACCESS_REASON_UNSPECIFIED, err
		}
		allowed, err := s.spiceDB.Can(ctx, decision)
		if err != nil {
			return openv1.FormAccessReason_FORM_ACCESS_REASON_UNSPECIFIED, err
		}
		if allowed {
			return openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED, nil
		}
	}
	return openv1.FormAccessReason_FORM_ACCESS_REASON_ROLE_NOT_ALLOWED, nil
}

func evaluateFormScheduleAccess(form *model.Form, preview bool, now time.Time) openv1.FormAccessReason {
	if preview {
		return openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED
	}
	if form.OpensAt != nil && now.Before(*form.OpensAt) {
		return openv1.FormAccessReason_FORM_ACCESS_REASON_NOT_YET_OPEN
	}
	if form.ClosesAt != nil && now.After(*form.ClosesAt) {
		return openv1.FormAccessReason_FORM_ACCESS_REASON_CLOSED
	}
	return openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED
}

func (s *FormService) evaluateFormSubmissionLimit(
	ctx context.Context,
	form *model.Form,
	check bool,
) (openv1.FormAccessReason, error) {
	if !check || form.MaxSubmissions == nil {
		return openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED, nil
	}
	count, err := s.countSubmissions(ctx, form.ID)
	if err != nil {
		return openv1.FormAccessReason_FORM_ACCESS_REASON_UNSPECIFIED, errs.Internal(err)
	}
	if count >= int64(*form.MaxSubmissions) {
		return openv1.FormAccessReason_FORM_ACCESS_REASON_MAX_SUBMISSIONS_REACHED, nil
	}
	return openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED, nil
}

func formPasswordRequired(service *FormService, form *model.Form, opts formAccessOptions) bool {
	return opts.enforcePassword && form.AccessPassword != nil && *form.AccessPassword != "" &&
		(opts.password == nil || *opts.password == "" || !service.verifyFormPassword(*opts.password, *form.AccessPassword))
}

func (s *FormService) countSubmissions(ctx context.Context, formID string) (int64, error) {
	var count int64
	if err := s.db.WithContext(ctx).
		Model(&model.FormSubmission{}).
		Where("form_id = ?", formID).
		Count(&count).Error; err != nil {
		return 0, err
	}

	return count, nil
}

func (s *FormService) hasMemberSubmission(ctx context.Context, formID, memberID string) (bool, error) {
	var count int64
	if err := s.db.WithContext(ctx).
		Model(&model.FormSubmission{}).
		Where("form_id = ? AND member_id = ?", formID, memberID).
		Count(&count).Error; err != nil {
		return false, err
	}

	return count > 0, nil
}

func formRoleCan(role string) (policyv1.Can, bool, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "admin":
		can, err := policyv1.Platform.IsAdmin()
		return can, true, err
	case "author":
		can, err := policyv1.Platform.IsAuthor()
		return can, true, err
	case "user":
		can, err := policyv1.Platform.IsUser()
		return can, true, err
	default:
		return policyv1.Can{}, false, nil
	}
}

func mapFormAccessReasonToError(reason openv1.FormAccessReason, draftAsNotFound bool) error {
	switch reason {
	case openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED:
		return nil
	case openv1.FormAccessReason_FORM_ACCESS_REASON_FORM_NOT_FOUND:
		return errs.NotFoundMsg("form not found")
	case openv1.FormAccessReason_FORM_ACCESS_REASON_FORM_NOT_PUBLISHED:
		if draftAsNotFound {
			return errs.NotFoundMsg("form not found")
		}
		return errs.FailedPrecondition(errs.MsgFormNotAccepting)
	case openv1.FormAccessReason_FORM_ACCESS_REASON_NOT_PUBLIC:
		return errs.NotFoundMsg("form not found")
	case openv1.FormAccessReason_FORM_ACCESS_REASON_AUTH_REQUIRED:
		return errs.AuthenticationRequired()
	case openv1.FormAccessReason_FORM_ACCESS_REASON_ROLE_NOT_ALLOWED:
		return errs.PermissionDenied("you do not have permission to access this form")
	case openv1.FormAccessReason_FORM_ACCESS_REASON_PASSWORD_REQUIRED:
		return errs.PermissionDenied("invalid form password")
	case openv1.FormAccessReason_FORM_ACCESS_REASON_NOT_YET_OPEN:
		return errs.FailedPrecondition(errs.MsgFormNotOpenYet)
	case openv1.FormAccessReason_FORM_ACCESS_REASON_CLOSED:
		return errs.FailedPrecondition(errs.MsgFormClosed)
	case openv1.FormAccessReason_FORM_ACCESS_REASON_MAX_SUBMISSIONS_REACHED:
		return errs.FailedPrecondition("submission limit reached")
	case openv1.FormAccessReason_FORM_ACCESS_REASON_ALREADY_SUBMITTED:
		return errs.FailedPrecondition(errs.MsgFormAlreadySubmitted)
	default:
		return errs.InternalMsg("failed to evaluate form access")
	}
}

func normalizeFormAccessContext(context openv1.FormAccessContext) openv1.FormAccessContext {
	switch context {
	case openv1.FormAccessContext_FORM_ACCESS_CONTEXT_EMBED:
		return openv1.FormAccessContext_FORM_ACCESS_CONTEXT_EMBED
	case openv1.FormAccessContext_FORM_ACCESS_CONTEXT_URL:
		return openv1.FormAccessContext_FORM_ACCESS_CONTEXT_URL
	default:
		return openv1.FormAccessContext_FORM_ACCESS_CONTEXT_URL
	}
}

func normalizeFormAccessTarget(target openv1.FormAccessTarget) openv1.FormAccessTarget {
	switch target {
	case openv1.FormAccessTarget_FORM_ACCESS_TARGET_DASHBOARD:
		return openv1.FormAccessTarget_FORM_ACCESS_TARGET_DASHBOARD
	case openv1.FormAccessTarget_FORM_ACCESS_TARGET_FORM:
		return openv1.FormAccessTarget_FORM_ACCESS_TARGET_FORM
	default:
		return openv1.FormAccessTarget_FORM_ACCESS_TARGET_FORM
	}
}
