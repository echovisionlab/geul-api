package ai

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Deps are the collaborators of the metadata-generation use cases; all are
// required.
type Deps struct {
	Units   mutation.UnitOfWork[Store]
	SpiceDB mutation.Authorizer
	Fence   identitystate.Fence
	// Audit is required by the mutation pipeline; metadata jobs are
	// operational rows and append no record.
	Audit domainaudit.Appender
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("ai dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the durable metadata-generation use cases. Interactive
// document editing is owned by the typed AI document protocol.
type Service struct {
	units     mutation.UnitOfWork[Store]
	spiceDB   mutation.Authorizer
	mutations *mutation.StoreRunner[Store]
}

// NewService returns the metadata-generation use cases over deps.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{units: deps.Units, spiceDB: deps.SpiceDB, mutations: mutations}, nil
}

func canUseAI(
	ctx context.Context,
	spiceDB authz.AuthorizationDecisionChecker,
	user *auth.UserInfo,
	target *Target,
) (bool, error) {
	if target != nil && target.Type == TargetWork {
		return checkAIAdmin(ctx, spiceDB, user)
	}
	if allowed, err := checkAIAdmin(ctx, spiceDB, user); err != nil {
		return false, err
	} else if allowed {
		return true, nil
	}
	if allowed, err := checkAIAuthor(ctx, spiceDB, user); err != nil {
		return false, err
	} else if allowed {
		return true, nil
	}

	if target == nil {
		return false, nil
	}
	targetID := strings.TrimSpace(target.ID)
	if targetID == "" {
		return false, nil
	}

	switch target.Type {
	case TargetPost:
		can, err := policyv1.Post.Edit(targetID)
		if err != nil {
			return false, nil //nolint:nilerr // a malformed target ID names no resource the requester may edit; deny it
		}
		return canManageAIResource(ctx, spiceDB, can, user)
	case TargetArtist:
		can, err := policyv1.Artist.Edit(targetID)
		if err != nil {
			return false, nil //nolint:nilerr // a malformed target ID names no resource the requester may edit; deny it
		}
		return canManageAIResource(ctx, spiceDB, can, user)
	default:
		// Pages, Forms, Releases, Labels, Campaigns and Email Templates
		// have no per-resource metadata authority.
		return false, nil
	}
}

func canManageAIResource(
	ctx context.Context,
	spiceDB authz.AuthorizationDecisionChecker,
	can policyv1.Can,
	user *auth.UserInfo,
) (bool, error) {
	if !can.Valid() || !isValidUUID(can.Resource().ID()) {
		return false, nil
	}
	return checkAICan(ctx, spiceDB, user, can)
}

func checkAIAdmin(ctx context.Context, spiceDB authz.AuthorizationDecisionChecker, user *auth.UserInfo) (bool, error) {
	can, err := policyv1.Platform.IsAdmin()
	if err != nil {
		return false, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	return checkAICan(ctx, spiceDB, user, can)
}

func checkAIAuthor(ctx context.Context, spiceDB authz.AuthorizationDecisionChecker, user *auth.UserInfo) (bool, error) {
	can, err := policyv1.Platform.IsAuthor()
	if err != nil {
		return false, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	return checkAICan(ctx, spiceDB, user, can)
}

func checkAICan(
	ctx context.Context,
	spiceDB authz.AuthorizationDecisionChecker,
	user *auth.UserInfo,
	can policyv1.Can,
) (bool, error) {
	if user == nil || user.IdentityID.String() == "" {
		return false, domainerr.AuthenticationRequired()
	}
	authorizationCtx := auth.WithUser(ctx, user)
	decision, err := auth.AuthorizationDecision(authorizationCtx, can)
	if err != nil {
		return false, domainerr.AuthenticationRequired().WithCause(err)
	}
	allowed, err := spiceDB.Can(authorizationCtx, decision)
	if err != nil {
		return false, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	return allowed, nil
}
