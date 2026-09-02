package ai

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Service exposes only the durable metadata-generation lifecycle. Interactive
// document editing is owned by the typed AI document protocol.
type Service struct {
	metadataAI *MetadataJobManager
}

var _ managev1connect.AIServiceHandler = (*Service)(nil)

func NewService(metadataAI *MetadataJobManager) *Service {
	dependencycheck.New("ai.Service").
		RequireNotNil(metadataAI, "metadataAI").
		Validate()
	return &Service{metadataAI: metadataAI}
}

func (s *Service) StartMetadataGeneration(
	ctx context.Context,
	req *connect.Request[managev1.StartMetadataGenerationRequest],
) (*connect.Response[managev1.StartMetadataGenerationResponse], error) {
	user := auth.GetUser(ctx)
	job, err := s.metadataAI.StartJob(ctx, user, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.StartMetadataGenerationResponse{Job: job}), nil
}

func (s *Service) GetMetadataGenerationJob(
	ctx context.Context,
	req *connect.Request[managev1.GetMetadataGenerationJobRequest],
) (*connect.Response[managev1.GetMetadataGenerationJobResponse], error) {
	user := auth.GetUser(ctx)
	job, err := s.metadataAI.GetJobForRequester(ctx, user, req.Msg.JobId)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.GetMetadataGenerationJobResponse{Job: job}), nil
}

func (s *Service) ResolveMetadataGenerationJob(
	ctx context.Context,
	req *connect.Request[managev1.ResolveMetadataGenerationJobRequest],
) (*connect.Response[managev1.ResolveMetadataGenerationJobResponse], error) {
	user := auth.GetUser(ctx)
	job, err := s.metadataAI.ResolveJobForRequester(ctx, user, req.Msg.JobId, req.Msg.Resolution)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.ResolveMetadataGenerationJobResponse{Job: job}), nil
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
			return false, nil
		}
		return canManageAIResource(ctx, spiceDB, can, user)
	case managev1.AIResourceType_AI_RESOURCE_TYPE_PAGE,
		managev1.AIResourceType_AI_RESOURCE_TYPE_FORM,
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
		return false, errs.DependencyUnavailable("SpiceDB")
	}
	return checkAICan(ctx, spiceDB, user, can)
}

func checkAIAuthor(ctx context.Context, spiceDB *auth.SpiceDBClient, user *auth.UserInfo) (bool, error) {
	can, err := policyv1.Platform.IsAuthor()
	if err != nil {
		return false, errs.DependencyUnavailable("SpiceDB")
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
		return false, errs.AuthenticationRequired()
	}
	authorizationCtx := auth.WithUser(ctx, user)
	decision, err := auth.AuthorizationDecision(authorizationCtx, can)
	if err != nil {
		return false, errs.AuthenticationRequired()
	}
	allowed, err := spiceDB.Can(authorizationCtx, decision)
	if err != nil {
		return false, errs.DependencyUnavailable("SpiceDB")
	}
	return allowed, nil
}
