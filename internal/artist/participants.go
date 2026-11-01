package artist

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Participant roles of an Artist.
const (
	ParticipantRoleOwner   = entityparticipant.RoleOwner
	ParticipantRoleManager = entityparticipant.RoleManager
)

// ListArtistParticipantsQuery lists an Artist's Owners and Managers.
type ListArtistParticipantsQuery struct {
	ArtistID string
}

// ListArtistParticipants returns the Artist's Owners, then Managers, to a
// caller who may manage its participants.
func (s *Service) ListArtistParticipants(ctx context.Context, query ListArtistParticipantsQuery) ([]entityparticipant.View, error) {
	if err := artistEntity.RequirePermission(ctx, s.spiceDB, query.ArtistID, policyv1.Artist.ManageParticipants); err != nil {
		return nil, err
	}
	var participants []entityparticipant.Participant
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		participants, err = store.ListParticipants(ctx, query.ArtistID)
		return err
	}); err != nil {
		return nil, err
	}
	return artistEntity.Views(ctx, s.spiceDB, s.members, query.ArtistID, participants)
}

// SetArtistParticipantCommand gives a Member Role (ParticipantRoleOwner or
// ParticipantRoleManager) on an Artist.
type SetArtistParticipantCommand struct {
	ArtistID string
	MemberID string
	Role     string
}

// SetArtistParticipant gives the Member the role, moving an existing
// participant between Owner and Manager.
func (s *Service) SetArtistParticipant(ctx context.Context, command SetArtistParticipantCommand) (entityparticipant.View, error) {
	if command.Role != ParticipantRoleOwner && command.Role != ParticipantRoleManager {
		return entityparticipant.View{}, domainerr.InvalidField("role", "must be Owner or Manager")
	}
	return s.participants.SetParticipant(ctx, s.members, entityparticipant.ParticipantChange{
		EntityID: command.ArtistID, MemberID: command.MemberID, Role: command.Role,
		Audit: artistParticipantAudit(command.ArtistID, command.MemberID),
	})
}

// RemoveArtistParticipantCommand removes a Member from an Artist.
type RemoveArtistParticipantCommand struct {
	ArtistID string
	MemberID string
}

// RemoveArtistParticipant removes the Member; the Artist keeps one Owner.
func (s *Service) RemoveArtistParticipant(ctx context.Context, command RemoveArtistParticipantCommand) error {
	return s.participants.RemoveParticipant(ctx, entityparticipant.ParticipantChange{
		EntityID: command.ArtistID, MemberID: command.MemberID,
		Audit: artistParticipantAudit(command.ArtistID, command.MemberID),
	})
}

func artistParticipantAudit(artistID, memberID string) entityparticipant.ParticipantAudit {
	return func(previous, next sharedtelemetry.AuditRelationship) mutation.Audit {
		return mutation.RequestAudit(sharedtelemetry.AuditArtistUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewArtistParticipantAuditRecord(m, artistID, memberID, previous, next)
		})
	}
}
