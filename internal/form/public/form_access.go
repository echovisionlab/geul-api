package public

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/form"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// AccessReason is why a visitor may or may not open a Form.
type AccessReason uint8

const (
	ReasonUnspecified AccessReason = iota
	ReasonAllowed
	ReasonFormNotFound
	ReasonFormNotPublished
	ReasonNotPublic
	ReasonAuthRequired
	ReasonRoleNotAllowed
	ReasonPasswordRequired
	ReasonNotYetOpen
	ReasonClosed
	ReasonMaxSubmissionsReached
	ReasonAlreadySubmitted
)

var accessReasonNames = [...]string{
	ReasonUnspecified:           "FORM_ACCESS_REASON_UNSPECIFIED",
	ReasonAllowed:               "FORM_ACCESS_REASON_ALLOWED",
	ReasonFormNotFound:          "FORM_ACCESS_REASON_FORM_NOT_FOUND",
	ReasonFormNotPublished:      "FORM_ACCESS_REASON_FORM_NOT_PUBLISHED",
	ReasonNotPublic:             "FORM_ACCESS_REASON_NOT_PUBLIC",
	ReasonAuthRequired:          "FORM_ACCESS_REASON_AUTH_REQUIRED",
	ReasonRoleNotAllowed:        "FORM_ACCESS_REASON_ROLE_NOT_ALLOWED",
	ReasonPasswordRequired:      "FORM_ACCESS_REASON_PASSWORD_REQUIRED",
	ReasonNotYetOpen:            "FORM_ACCESS_REASON_NOT_YET_OPEN",
	ReasonClosed:                "FORM_ACCESS_REASON_CLOSED",
	ReasonMaxSubmissionsReached: "FORM_ACCESS_REASON_MAX_SUBMISSIONS_REACHED",
	ReasonAlreadySubmitted:      "FORM_ACCESS_REASON_ALREADY_SUBMITTED",
}

// String names the reason as the API does.
func (r AccessReason) String() string {
	if int(r) < len(accessReasonNames) {
		return accessReasonNames[r]
	}
	return fmt.Sprintf("AccessReason(%d)", uint8(r))
}

// AccessContext is where a Form is opened: its own URL or an embed.
type AccessContext uint8

const (
	ContextURL AccessContext = iota + 1
	ContextEmbed
)

// validateShareToken validates that the share token allows access to the form
// for one of the given share link entity types.
func (s *Service) validateShareToken(
	ctx context.Context,
	found *form.Form,
	shareToken *string,
	sharePassword *string,
	allowedTypes ...string,
) formShareTokenState {
	if shareToken == nil || *shareToken == "" {
		return formShareTokenState{}
	}

	var shareLink ShareLink
	err := s.read(ctx, func(store Store) error {
		var err error
		shareLink, err = store.ShareLink(ctx, *shareToken)
		return err
	})
	if err != nil {
		if !errors.Is(err, ErrShareLinkNotFound) {
			slog.ErrorContext(ctx, "Share link lookup failed; treating the share token as invalid", "domain", "form", "event", "form.operation_failed", "operation", "load_share_link", "outcome", "failed", failurecause.Attr(err, *shareToken))
		}
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
		if err != nil {
			slog.ErrorContext(ctx, "Share link password hash verification failed; treating the share token as invalid", "domain", "form", "event", "form.operation_failed", "operation", "verify_share_password", "outcome", "failed", failurecause.Attr(err, *sharePassword, *shareLink.PasswordHash))
		}
		if err != nil || !matched {
			return formShareTokenState{}
		}
	}

	// Check entity ID
	if shareLink.EntityID != found.ID {
		return formShareTokenState{}
	}

	if !slices.Contains(allowedTypes, shareLink.EntityType) {
		return formShareTokenState{}
	}

	return formShareTokenState{valid: true}
}

func (s *Service) findFormBySlugOrID(ctx context.Context, slugOrID string) (form.Form, error) {
	var found form.Form
	err := s.read(ctx, func(store Store) error {
		var err error
		found, err = store.FindForm(ctx, form.LocatorOf(slugOrID), slugOrID)
		return err
	})
	if err != nil {
		if errors.Is(err, form.ErrFormNotFound) {
			return form.Form{}, errFormNotFound()
		}
		return form.Form{}, err
	}
	return found, nil
}

// submissionReads are the submission reads of an access check: the Store of
// a submission's transaction, or reads of their own outside one.
type submissionReads interface {
	CountSubmissions(ctx context.Context, formID string) (int64, error)
	HasMemberSubmission(ctx context.Context, formID, memberID string) (bool, error)
}

// reads runs each submission read in a read snapshot of its own.
func (s *Service) reads() submissionReads { return ownReads{s} }

type ownReads struct{ service *Service }

func (r ownReads) CountSubmissions(ctx context.Context, formID string) (int64, error) {
	var count int64
	err := r.service.read(ctx, func(store Store) error {
		var err error
		count, err = store.CountSubmissions(ctx, formID)
		return err
	})
	return count, err
}

func (r ownReads) HasMemberSubmission(ctx context.Context, formID, memberID string) (bool, error) {
	var exists bool
	err := r.service.read(ctx, func(store Store) error {
		var err error
		exists, err = store.HasMemberSubmission(ctx, formID, memberID)
		return err
	})
	return exists, err
}

type formAccessOptions struct {
	context                  AccessContext
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
func (s *Service) enforceFormAccess(ctx context.Context, reads submissionReads, found *form.Form, opts formAccessOptions) error {
	reason, err := s.evaluateFormAccess(ctx, reads, found, opts)
	if err != nil {
		return err
	}

	return mapFormAccessReasonToError(reason, opts.draftAsNotFound)
}

func (s *Service) evaluateFormAccess(
	ctx context.Context,
	reads submissionReads,
	found *form.Form,
	opts formAccessOptions,
) (AccessReason, error) {
	user := auth.GetUser(ctx)

	isPublished := found.Status == form.StatusPublished
	if !isPublished && !opts.hasValidPreviewToken {
		return ReasonFormNotPublished, nil
	}

	// Public URL access is allowed only when explicitly enabled,
	// unless this is preview-token access.
	if opts.context != ContextEmbed &&
		!opts.hasValidPreviewToken &&
		!found.IsPublic {
		return ReasonNotPublic, nil
	}

	requireAuthActive := found.RequireAuth != nil && *found.RequireAuth && !opts.bypassAuth
	allowedRolesActive := len(found.AllowedRoles) > 0 && !opts.bypassRoles

	if requireAuthActive && (user == nil || !user.Authenticated || user.Banned) {
		return ReasonAuthRequired, nil
	}
	if reason, handled, err := evaluateDuplicateFormSubmission(
		ctx, reads, found, opts, user, requireAuthActive || allowedRolesActive,
	); handled || err != nil {
		return reason, err
	}
	if reason, err := s.evaluateFormRoleAccess(
		ctx, user, found.AllowedRoles, allowedRolesActive,
	); err != nil || reason != ReasonAllowed {
		if err != nil {
			return ReasonUnspecified, err
		}
		return reason, nil
	}
	if reason := evaluateFormScheduleAccess(
		found, opts.hasValidPreviewToken, time.Now(),
	); reason != ReasonAllowed {
		return reason, nil
	}
	if reason, err := evaluateFormSubmissionLimit(
		ctx, reads, found, opts.checkSubmissionLimit,
	); err != nil || reason != ReasonAllowed {
		return reason, err
	}
	if formPasswordRequired(ctx, s, found, opts) {
		return ReasonPasswordRequired, nil
	}

	return ReasonAllowed, nil
}

func evaluateDuplicateFormSubmission(
	ctx context.Context,
	reads submissionReads,
	found *form.Form,
	opts formAccessOptions,
	user *auth.UserInfo,
	requiresAuthenticatedIdentity bool,
) (AccessReason, bool, error) {
	shouldCheck := opts.checkDuplicateSubmission && requiresAuthenticatedIdentity && !opts.hasValidPreviewToken &&
		user != nil && found.AllowDuplicateSubmission != nil && !*found.AllowDuplicateSubmission
	if !shouldCheck {
		return ReasonAllowed, false, nil
	}
	exists, err := reads.HasMemberSubmission(ctx, found.ID, user.MemberID.String())
	if err != nil {
		return ReasonUnspecified, true, err
	}
	if exists {
		return ReasonAlreadySubmitted, true, nil
	}
	return ReasonAllowed, false, nil
}

func (s *Service) evaluateFormRoleAccess(
	ctx context.Context,
	user *auth.UserInfo,
	allowedRoles []string,
	active bool,
) (AccessReason, error) {
	if !active {
		return ReasonAllowed, nil
	}
	if user == nil {
		return ReasonAuthRequired, nil
	}
	if !user.Authenticated || user.Banned || strings.TrimSpace(user.IdentityID.String()) == "" {
		return ReasonAuthRequired, nil
	}
	for _, role := range allowedRoles {
		can, ok, err := formRoleCan(role)
		if err != nil {
			return ReasonUnspecified, err
		}
		if !ok {
			continue
		}
		decision, err := auth.AuthorizationDecision(ctx, can)
		if err != nil {
			return ReasonUnspecified, err
		}
		allowed, err := s.spiceDB.Can(ctx, decision)
		if err != nil {
			return ReasonUnspecified, err
		}
		if allowed {
			return ReasonAllowed, nil
		}
	}
	return ReasonRoleNotAllowed, nil
}

func evaluateFormScheduleAccess(found *form.Form, preview bool, now time.Time) AccessReason {
	if preview {
		return ReasonAllowed
	}
	if found.OpensAt != nil && now.Before(*found.OpensAt) {
		return ReasonNotYetOpen
	}
	if found.ClosesAt != nil && now.After(*found.ClosesAt) {
		return ReasonClosed
	}
	return ReasonAllowed
}

func evaluateFormSubmissionLimit(
	ctx context.Context,
	reads submissionReads,
	found *form.Form,
	check bool,
) (AccessReason, error) {
	if !check || found.MaxSubmissions == nil {
		return ReasonAllowed, nil
	}
	count, err := reads.CountSubmissions(ctx, found.ID)
	if err != nil {
		return ReasonUnspecified, err
	}
	if count >= int64(*found.MaxSubmissions) {
		return ReasonMaxSubmissionsReached, nil
	}
	return ReasonAllowed, nil
}

func formPasswordRequired(ctx context.Context, service *Service, found *form.Form, opts formAccessOptions) bool {
	return opts.enforcePassword && found.HasPassword() &&
		(opts.password == nil || *opts.password == "" || !service.verifyFormPassword(ctx, *opts.password, *found.AccessPassword))
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

func mapFormAccessReasonToError(reason AccessReason, draftAsNotFound bool) error {
	switch reason {
	case ReasonAllowed:
		return nil
	case ReasonFormNotFound:
		return errFormNotFound()
	case ReasonFormNotPublished:
		if draftAsNotFound {
			return errFormNotFound()
		}
		return domainerr.Precondition("form is not accepting submissions")
	case ReasonNotPublic:
		return errFormNotFound()
	case ReasonAuthRequired:
		return domainerr.AuthenticationRequired()
	case ReasonRoleNotAllowed:
		return domainerr.Denied("you do not have permission to access this form")
	case ReasonPasswordRequired:
		return domainerr.Denied("invalid form password")
	case ReasonNotYetOpen:
		return domainerr.Precondition("form is not open yet")
	case ReasonClosed:
		return domainerr.Precondition("form is closed")
	case ReasonMaxSubmissionsReached:
		return domainerr.Precondition("submission limit reached")
	case ReasonAlreadySubmitted:
		return domainerr.Precondition(msgFormAlreadySubmitted)
	default:
		return fmt.Errorf("failed to evaluate form access: reason %v", reason)
	}
}

const msgFormAlreadySubmitted = "you have already submitted this form"

// errFormNotFound answers every Form a visitor may not see.
func errFormNotFound() error {
	return domainerr.New(domainerr.NotFound, "", "form not found")
}
