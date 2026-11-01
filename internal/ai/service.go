package ai

import (
	"context"
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Deps are the collaborators of the metadata-generation use cases; all are
// required.
type Deps struct {
	DB      *gorm.DB
	SpiceDB *auth.SpiceDBClient
	// Audit is required by the mutation pipeline; metadata jobs are
	// operational rows and append no record.
	Audit     domainaudit.Appender
	Publisher AsyncPublisher
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Audit": d.Audit == nil, "Publisher": d.Publisher == nil,
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
	db        *gorm.DB
	spiceDB   *auth.SpiceDBClient
	publisher AsyncPublisher
	mutations *mutation.Runner
}

// NewService returns the metadata-generation use cases over deps.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{db: deps.DB, spiceDB: deps.SpiceDB, publisher: deps.Publisher, mutations: mutations}, nil
}

func canUseAI(
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
	user *auth.UserInfo,
	target *managev1.AIResourceTarget,
) (bool, error) {
	if target != nil && target.Type == managev1.AIResourceType_AI_RESOURCE_TYPE_WORK {
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
	targetID := strings.TrimSpace(target.Id)
	if targetID == "" {
		return false, nil
	}

	switch target.Type {
	case managev1.AIResourceType_AI_RESOURCE_TYPE_POST:
		can, err := policyv1.Post.Edit(targetID)
		if err != nil {
			return false, nil //nolint:nilerr // a malformed target ID names no resource the requester may edit; deny it
		}
		return canManageAIResource(ctx, spiceDB, can, user)
	case managev1.AIResourceType_AI_RESOURCE_TYPE_ARTIST:
		can, err := policyv1.Artist.Edit(targetID)
		if err != nil {
			return false, nil //nolint:nilerr // a malformed target ID names no resource the requester may edit; deny it
		}
		return canManageAIResource(ctx, spiceDB, can, user)
	case managev1.AIResourceType_AI_RESOURCE_TYPE_PAGE,
		managev1.AIResourceType_AI_RESOURCE_TYPE_FORM,
		managev1.AIResourceType_AI_RESOURCE_TYPE_RELEASE,
		managev1.AIResourceType_AI_RESOURCE_TYPE_LABEL,
		managev1.AIResourceType_AI_RESOURCE_TYPE_CAMPAIGN,
		managev1.AIResourceType_AI_RESOURCE_TYPE_EMAIL_TEMPLATE:
		return false, nil
	default:
		return false, nil
	}
}

func canManageAIResource(
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
	can policyv1.Can,
	user *auth.UserInfo,
) (bool, error) {
	if !can.Valid() || !isValidUUID(can.Resource().ID()) {
		return false, nil
	}
	return checkAICan(ctx, spiceDB, user, can)
}

func checkAIAdmin(ctx context.Context, spiceDB *auth.SpiceDBClient, user *auth.UserInfo) (bool, error) {
	can, err := policyv1.Platform.IsAdmin()
	if err != nil {
		return false, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	return checkAICan(ctx, spiceDB, user, can)
}

func checkAIAuthor(ctx context.Context, spiceDB *auth.SpiceDBClient, user *auth.UserInfo) (bool, error) {
	can, err := policyv1.Platform.IsAuthor()
	if err != nil {
		return false, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	return checkAICan(ctx, spiceDB, user, can)
}

func checkAICan(
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
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
