package label

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
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
func (s *Service) ListLabelParticipants(ctx context.Context, query ListLabelParticipantsQuery) (_ []entityparticipant.View, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", query.LabelID))

	if err := labelEntity.RequirePermission(ctx, s.spiceDB, query.LabelID, policyv1.Label.ManageParticipants); err != nil {
		return nil, err
	}
	var participants []entityparticipant.Participant
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		participants, err = store.ListParticipants(ctx, query.LabelID)
		return err
	}); err != nil {
		return nil, err
	}
	return labelEntity.Views(ctx, s.spiceDB, s.members, query.LabelID, participants)
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
func (s *Service) SetLabelParticipant(ctx context.Context, command SetLabelParticipantCommand) (_ entityparticipant.View, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", command.LabelID))

	if command.Role != ParticipantRoleOwner && command.Role != ParticipantRoleManager {
		return entityparticipant.View{}, domainerr.InvalidField("role", "must be Owner or Manager")
	}
	return s.participants.SetParticipant(ctx, s.members, entityparticipant.ParticipantChange{
		EntityID: command.LabelID, MemberID: command.MemberID, Role: command.Role,
		Audit: labelParticipantAudit(command.LabelID, command.MemberID),
	})
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
