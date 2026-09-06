package label

import (
	"context"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

func (s *LabelService) ListLabelParticipants(
	ctx context.Context,
	req *connect.Request[managev1.ListLabelParticipantsRequest],
) (*connect.Response[managev1.ListLabelParticipantsResponse], error) {
	if err := requireLabelPermission(ctx, s.spiceDB, req.Msg.LabelId, policyv1.Label.ManageParticipants); err != nil {
		return nil, err
	}
	views, err := loadEntityParticipantViews(ctx, s.db, s.spiceDB, s.members, labelParticipantSpec(), req.Msg.LabelId)
	if err != nil {
		return nil, err
	}
	participants := mapEntityParticipantViews(views, labelParticipantFromView)
	return connect.NewResponse(&managev1.ListLabelParticipantsResponse{Participants: participants}), nil
}

func (s *LabelService) SetLabelParticipant(
	ctx context.Context,
	req *connect.Request[managev1.SetLabelParticipantRequest],
) (*connect.Response[managev1.LabelParticipant], error) {
	role, err := labelParticipantRole(req.Msg.Role)
	if err != nil {
		return nil, err
	}
	view, err := setEntityParticipantViewWithAudit(ctx, s.db, s.spiceDB, s.members, labelParticipantSpec(), req.Msg.LabelId, req.Msg.MemberId, role, func(ctx context.Context, tx *gorm.DB, previous, next string) error {
		return s.appendLabelParticipantAudit(ctx, tx, req.Msg.LabelId, req.Msg.MemberId, labelAuditRelationship(previous), labelAuditRelationship(next))
	})
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
) (*connect.Response[managev1.DeleteResponse], error) {
	return removeEntityParticipantResponseWithAudit(ctx, s.db, s.spiceDB, labelParticipantSpec(), req.Msg.LabelId, req.Msg.MemberId, func(ctx context.Context, tx *gorm.DB, previous, next string) error {
		return s.appendLabelParticipantAudit(ctx, tx, req.Msg.LabelId, req.Msg.MemberId, labelAuditRelationship(previous), labelAuditRelationship(next))
	})
}

func labelAuditRelationship(role string) sharedtelemetry.AuditRelationship {
	if role == participantRoleOwner {
		return sharedtelemetry.AuditRelationshipOwner
	}
	if role == participantRoleManager {
		return sharedtelemetry.AuditRelationshipManager
	}
	return sharedtelemetry.AuditRelationshipNone
}

func labelParticipantFromView(view entityParticipantView) *managev1.LabelParticipant {
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

// =============================================================================
// Utility
// =============================================================================
