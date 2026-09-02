package menu

import (
	"fmt"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func normalizeUserRoleList(field string, roles []string) ([]string, error) {
	if len(roles) == 0 {
		return nil, nil
	}

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
