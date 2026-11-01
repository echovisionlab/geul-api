package public

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isValidUUID(value string) bool { return uuidPattern.MatchString(value) }

func hasDraftResourceView(
	ctx context.Context,
	checker authz.AuthorizationDecisionChecker,
	resourceID string,
) (bool, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.Banned || strings.TrimSpace(user.IdentityID.String()) == "" {
		return false, nil
	}
	can, err := policyv1.Post.View(resourceID)
	if err != nil {
		return false, err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, err
	}
	return checker.Can(ctx, decision)
}

func requireDraftShareLinkAccess(
	ctx context.Context,
	store Store,
	shareToken string,
	sharePassword string,
	entityID string,
	entityName string,
) (*mediaasset.ContentDownloadShareLinkWitness, error) {
	if shareToken == "" {
		return nil, domainerr.New(domainerr.NotFound, "", entityName+" not found")
	}
	link, err := store.ShareLink(ctx, shareToken, sharePassword, entityID)
	if errors.Is(err, ErrShareLinkNotFound) {
		return nil, domainerr.New(domainerr.NotFound, "", entityName+" not found")
	}
	if err != nil {
		return nil, fmt.Errorf("validate %s share link: %w", entityName, err)
	}
	return link, nil
}
