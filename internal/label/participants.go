package label

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

const (
	participantRoleOwner   = entityparticipant.RoleOwner
	participantRoleManager = entityparticipant.RoleManager
)

func (s *LabelService) ListLabelParticipants(
	ctx context.Context,
	req *connect.Request[managev1.ListLabelParticipantsRequest],
) (_ *connect.Response[managev1.ListLabelParticipantsResponse], returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", req.Msg.GetLabelId()))

	if err := labelEntity.RequirePermission(ctx, s.spiceDB, req.Msg.LabelId, policyv1.Label.ManageParticipants); err != nil {
		return nil, err
	}
	views, err := labelEntity.ListParticipants(ctx, s.db, s.spiceDB, s.members, req.Msg.LabelId)
	if err != nil {
		return nil, err
	}
	participants := entityparticipant.MapViews(views, labelParticipantFromView)
	return connect.NewResponse(&managev1.ListLabelParticipantsResponse{Participants: participants}), nil
}

func (s *LabelService) SetLabelParticipant(
	ctx context.Context,
	req *connect.Request[managev1.SetLabelParticipantRequest],
) (_ *connect.Response[managev1.LabelParticipant], returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", req.Msg.GetLabelId()))

	role, err := labelParticipantRole(req.Msg.Role)
	if err != nil {
		return nil, err
	}
	view, err := labelEntity.SetParticipant(ctx, s.db, s.spiceDB, s.members, req.Msg.LabelId, req.Msg.MemberId, role, s.labelParticipantAudit(req.Msg.LabelId, req.Msg.MemberId))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.LabelParticipant{
		Member: view.Member, Role: req.Msg.Role, CreatedAt: timestamppb.New(view.CreatedAt), HasEffectiveAuthority: view.HasEffectiveAuthority,
	}), nil
}

func (s *LabelService) RemoveLabelParticipant(
	ctx context.Context,
	req *connect.Request[managev1.RemoveLabelParticipantRequest],
) (_ *connect.Response[managev1.DeleteResponse], returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", req.Msg.GetLabelId()))

	if err := labelEntity.RemoveParticipant(ctx, s.db, s.spiceDB, req.Msg.LabelId, req.Msg.MemberId, s.labelParticipantAudit(req.Msg.LabelId, req.Msg.MemberId)); err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

func (s *LabelService) labelParticipantAudit(labelID, memberID string) entityparticipant.Audit {
	return func(ctx context.Context, tx *gorm.DB, previous, next sharedtelemetry.AuditRelationship) error {
		return s.appendLabelParticipantAudit(ctx, tx, labelID, memberID, previous, next)
	}
}

func labelParticipantFromView(view entityparticipant.View) *managev1.LabelParticipant {
	role := managev1.LabelParticipantRole_LABEL_PARTICIPANT_ROLE_MANAGER
	if view.Role == participantRoleOwner {
		role = managev1.LabelParticipantRole_LABEL_PARTICIPANT_ROLE_OWNER
	}
	return &managev1.LabelParticipant{
		Member: view.Member, Role: role, CreatedAt: timestamppb.New(view.CreatedAt),
		HasEffectiveAuthority: view.HasEffectiveAuthority,
	}
}

func labelParticipantRole(role managev1.LabelParticipantRole) (string, error) {
	switch role {
	case managev1.LabelParticipantRole_LABEL_PARTICIPANT_ROLE_OWNER:
		return participantRoleOwner, nil
	case managev1.LabelParticipantRole_LABEL_PARTICIPANT_ROLE_MANAGER:
		return participantRoleManager, nil
	default:
		return "", errs.InvalidArgument("role", "must be Owner or Manager")
	}
}
