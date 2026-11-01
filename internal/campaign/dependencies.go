package campaign

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// CollaborationPermissionChecker is the fully consistent authorization read
// required by Campaign collaboration mutations.
type CollaborationPermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
}

func campaignEditCan(campaignID string) (policyv1.Can, error) {
	if _, err := uuidutil.ParseCanonical(campaignID, "campaign id"); err != nil {
		return policyv1.Can{}, err
	}
	return policyv1.Campaign.Edit(campaignID)
}

func ptrStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func nullableTrimmedString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func campaignEmailTestActorID(ctx context.Context) string {
	if user := auth.GetUser(ctx); user != nil && strings.TrimSpace(user.MemberID.String()) != "" {
		return user.MemberID.String()
	}
	return "system"
}

func requireDependencies(name string, absent map[string]bool) error {
	var missing []string
	for field, isAbsent := range absent {
		if isAbsent {
			missing = append(missing, field)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	slices.Sort(missing)
	return errors.New(name + " dependencies are required: " + strings.Join(missing, ", "))
}
