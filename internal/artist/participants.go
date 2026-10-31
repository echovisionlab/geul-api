package artist

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
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
func (s *Service) ListArtistParticipants(ctx context.Context, query ListArtistParticipantsQuery) ([]*managev1.ArtistParticipant, error) {
	if err := artistEntity.RequirePermission(ctx, s.spiceDB, query.ArtistID, policyv1.Artist.ManageParticipants); err != nil {
		return nil, err
	}
	views, err := artistEntity.ListParticipants(ctx, s.db, s.spiceDB, s.members, query.ArtistID)
	if err != nil {
		return nil, err
	}
	return entityparticipant.MapViews(views, artistParticipantFromView), nil
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
func (s *Service) SetArtistParticipant(ctx context.Context, command SetArtistParticipantCommand) (*managev1.ArtistParticipant, error) {
	if command.Role != ParticipantRoleOwner && command.Role != ParticipantRoleManager {
		return nil, domainerr.InvalidField("role", "must be Owner or Manager")
	}
	view, err := s.participants.SetParticipant(ctx, s.members, entityparticipant.ParticipantChange{
		EntityID: command.ArtistID, MemberID: command.MemberID, Role: command.Role,
		Audit: artistParticipantAudit(command.ArtistID, command.MemberID),
	})
	if err != nil {
		return nil, err
	}
	return artistParticipantFromView(view), nil
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

func artistParticipantFromView(view entityparticipant.View) *managev1.ArtistParticipant {
	role := managev1.ArtistParticipantRole_ARTIST_PARTICIPANT_ROLE_MANAGER
	if view.Role == ParticipantRoleOwner {
		role = managev1.ArtistParticipantRole_ARTIST_PARTICIPANT_ROLE_OWNER
	}
	return &managev1.ArtistParticipant{
		Member: view.Member, Role: role, CreatedAt: timestamppb.New(view.CreatedAt),
		HasEffectiveAuthority: view.HasEffectiveAuthority,
	}
}
