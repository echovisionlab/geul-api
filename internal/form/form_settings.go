package form

import (
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/pathslug"
)

func validateFormSettings(form *model.Form) error {
	if form == nil {
		return domainerr.New(domainerr.Invalid, "", "form settings are required")
	}

	switch form.Status {
	case model.FormStatusDraft, model.FormStatusPublished:
	default:
		return domainerr.InvalidField("status", "must be draft or published")
	}

	if form.Slug != nil {
		slug := strings.TrimSpace(*form.Slug)
		if slug == "" {
			return domainerr.InvalidField("slug", "must not be blank")
		}
		if err := pathslug.Validate(slug); err != nil {
			return err
		}
	}
	if form.IsPublic && form.Slug == nil {
		return domainerr.InvalidField("is_public", "requires a slug")
	}
	if form.MaxSubmissions != nil && *form.MaxSubmissions < 1 {
		return domainerr.InvalidField("max_submissions", "must be at least 1")
	}
	if form.OpensAt != nil && form.ClosesAt != nil && !form.OpensAt.Before(*form.ClosesAt) {
		return domainerr.InvalidField("closes_at", "must be after opens_at")
	}
	requireAuth := form.RequireAuth != nil && *form.RequireAuth
	if len(form.AllowedRoles) > 0 && !requireAuth {
		return domainerr.InvalidField("allowed_roles", "requires require_auth")
	}
	return nil
}
