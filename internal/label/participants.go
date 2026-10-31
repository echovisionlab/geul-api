package label

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Participant roles of a Label.
const (
	ParticipantRoleOwner   = entityparticipant.RoleOwner
	ParticipantRoleManager = entityparticipant.RoleManager
)

// ListLabelParticipantsQuery lists a Label's Owners and Managers.
type ListLabelParticipantsQuery struct {
	LabelID string
}

// ListLabelParticipants returns the Label's Owners, then Managers, to a
// caller who may manage its participants.
func (s *Service) ListLabelParticipants(ctx context.Context, query ListLabelParticipantsQuery) (_ []*managev1.LabelParticipant, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", query.LabelID))

	if err := labelEntity.RequirePermission(ctx, s.spiceDB, query.LabelID, policyv1.Label.ManageParticipants); err != nil {
		return nil, err
	}
	views, err := labelEntity.ListParticipants(ctx, s.db, s.spiceDB, s.members, query.LabelID)
	if err != nil {
		return nil, err
	}
	return entityparticipant.MapViews(views, labelParticipantFromView), nil
}

// SetLabelParticipantCommand gives a Member Role (ParticipantRoleOwner or
// ParticipantRoleManager) on a Label.
type SetLabelParticipantCommand struct {
	LabelID  string
	MemberID string
	Role     string
}

// SetLabelParticipant gives the Member the role, moving an existing
// participant between Owner and Manager.
func (s *Service) SetLabelParticipant(ctx context.Context, command SetLabelParticipantCommand) (_ *managev1.LabelParticipant, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", command.LabelID))

	if command.Role != ParticipantRoleOwner && command.Role != ParticipantRoleManager {
		return nil, domainerr.InvalidField("role", "must be Owner or Manager")
	}
	view, err := s.participants.SetParticipant(ctx, s.members, entityparticipant.ParticipantChange{
		EntityID: command.LabelID, MemberID: command.MemberID, Role: command.Role,
		Audit: labelParticipantAudit(command.LabelID, command.MemberID),
	})
	if err != nil {
		return nil, err
	}
	return labelParticipantFromView(view), nil
}

// RemoveLabelParticipantCommand removes a Member from a Label.
type RemoveLabelParticipantCommand struct {
	LabelID  string
	MemberID string
}

// RemoveLabelParticipant removes the Member; the Label keeps one Owner.
func (s *Service) RemoveLabelParticipant(ctx context.Context, command RemoveLabelParticipantCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", command.LabelID))

	return s.participants.RemoveParticipant(ctx, entityparticipant.ParticipantChange{
		EntityID: command.LabelID, MemberID: command.MemberID,
		Audit: labelParticipantAudit(command.LabelID, command.MemberID),
	})
}

func labelParticipantAudit(labelID, memberID string) entityparticipant.ParticipantAudit {
	return func(previous, next sharedtelemetry.AuditRelationship) mutation.Audit {
		return mutation.RequestAudit(sharedtelemetry.AuditLabelUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewLabelParticipantAuditRecord(m, labelID, memberID, previous, next)
		})
	}
}

func labelParticipantFromView(view entityparticipant.View) *managev1.LabelParticipant {
	role := managev1.LabelParticipantRole_LABEL_PARTICIPANT_ROLE_MANAGER
	if view.Role == ParticipantRoleOwner {
		role = managev1.LabelParticipantRole_LABEL_PARTICIPANT_ROLE_OWNER
	}
	return &managev1.LabelParticipant{
		Member: view.Member, Role: role, CreatedAt: timestamppb.New(view.CreatedAt),
		HasEffectiveAuthority: view.HasEffectiveAuthority,
	}
}
