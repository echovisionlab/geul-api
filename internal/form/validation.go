package form

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func IsValidUUID(value string) bool {
	return uuidPattern.MatchString(value)
}

func isSlugAvailable(ctx context.Context, store Store, slug, excludeID string) (bool, error) {
	if slug == "" {
		return true, nil
	}
	taken, err := store.SlugTaken(ctx, slug, excludeID)
	return !taken, err
}

// ensureSlugAvailable requires slug to be free among Forms and the routes of
// other resources.
func ensureSlugAvailable(ctx context.Context, store Store, slug, excludeID string) error {
	available, err := isSlugAvailable(ctx, store, slug, excludeID)
	if err != nil {
		return err
	}
	if !available {
		return domainerr.Duplicate("form", "slug", slug)
	}
	if available, err = store.RouteAvailable(ctx, slug); err != nil {
		return err
	}
	if !available {
		return domainerr.Duplicate("form", "slug", slug)
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
			return nil, domainerr.InvalidField(field, fmt.Sprintf("invalid role: %s", strings.TrimSpace(role)))
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, canonical)
	}
	return normalized, nil
}
