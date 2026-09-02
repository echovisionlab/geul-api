package form

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func IsValidUUID(value string) bool {
	return uuidPattern.MatchString(value)
}

func validateSlugWithoutSlash(slug string) error {
	if strings.Contains(slug, "/") {
		return errs.InvalidArgument("slug", "must not contain '/'")
	}
	return nil
}

func isSlugAvailable(ctx context.Context, db *gorm.DB, slug, excludeID string) (bool, error) {
	if slug == "" {
		return true, nil
	}
	query := db.WithContext(ctx).Model(&model.Form{}).Where("slug = ?", slug)
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, errs.Internal(err)
	}
	return count == 0, nil
}

func ensureSlugAvailable(ctx context.Context, db *gorm.DB, slug, excludeID string) error {
	available, err := isSlugAvailable(ctx, db, slug, excludeID)
	if err != nil {
		return err
	}
	if !available {
		return errs.SlugAlreadyExists("form", slug)
	}
	return nil
}

func normalizeUserRoleList(field string, roles []string) ([]string, error) {
	seen := make(map[string]struct{}, len(roles))
	normalized := make([]string, 0, len(roles))
	for _, role := range roles {
		canonical := strings.ToLower(strings.TrimSpace(role))
		switch canonical {
		case policyv1.Role.Admin().ID(), policyv1.Role.Author().ID(), policyv1.Role.User().ID():
		default:
			return nil, errs.InvalidArgument(field, fmt.Sprintf("invalid role: %s", strings.TrimSpace(role)))
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, canonical)
	}
	return normalized, nil
}

var FormFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps,
			SearchColumns: []string{FormSourceTitleSQL("form")},
		},
		"status": {
			Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps,
			EnumValues: []string{
				managev1.FormStatus_FORM_STATUS_DRAFT.String(),
				managev1.FormStatus_FORM_STATUS_PUBLISHED.String(),
			},
		},
	},
}
