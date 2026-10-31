package form

import (
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/pathslug"
)

func validateFormSettings(form *model.Form) error {
	if form == nil {
		return errs.InvalidArgumentMsg("form settings are required")
	}

	switch form.Status {
	case model.FormStatusDraft, model.FormStatusPublished:
	default:
		return errs.InvalidArgument("status", "must be draft or published")
	}

	if form.Slug != nil {
		slug := strings.TrimSpace(*form.Slug)
		if slug == "" {
			return errs.InvalidArgument("slug", "must not be blank")
		}
		if err := pathslug.Validate(slug); err != nil {
			return err
		}
	}
	if form.IsPublic && form.Slug == nil {
		return errs.InvalidArgument("is_public", "requires a slug")
	}
	if form.MaxSubmissions != nil && *form.MaxSubmissions < 1 {
		return errs.InvalidArgument("max_submissions", "must be at least 1")
	}
	if form.OpensAt != nil && form.ClosesAt != nil && !form.OpensAt.Before(*form.ClosesAt) {
		return errs.InvalidArgument("closes_at", "must be after opens_at")
	}
	requireAuth := form.RequireAuth != nil && *form.RequireAuth
	if len(form.AllowedRoles) > 0 && !requireAuth {
		return errs.InvalidArgument("allowed_roles", "requires require_auth")
	}
	return nil
}
