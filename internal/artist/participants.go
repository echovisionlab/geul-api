package artist

import (
	"context"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	errs "github.com/echovisionlab/geul-api/internal/errors"
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

// ArtistService implements the ArtistService Connect handler
func (s *ArtistService) ListArtistParticipants(
	ctx context.Context,
	req *connect.Request[managev1.ListArtistParticipantsRequest],
) (*connect.Response[managev1.ListArtistParticipantsResponse], error) {
	if err := artistEntity.RequirePermission(ctx, s.spiceDB, req.Msg.ArtistId, policyv1.Artist.ManageParticipants); err != nil {
		return nil, err
	}
	views, err := artistEntity.ListParticipants(ctx, s.db, s.spiceDB, s.members, req.Msg.ArtistId)
	if err != nil {
		return nil, err
	}
	participants := entityparticipant.MapViews(views, artistParticipantFromView)
	return connect.NewResponse(&managev1.ListArtistParticipantsResponse{Participants: participants}), nil
}

func (s *ArtistService) SetArtistParticipant(
	ctx context.Context,
	req *connect.Request[managev1.SetArtistParticipantRequest],
) (*connect.Response[managev1.ArtistParticipant], error) {
	role, err := artistParticipantRole(req.Msg.Role)
	if err != nil {
		return nil, err
	}
	view, err := artistEntity.SetParticipant(ctx, s.db, s.spiceDB, s.members, req.Msg.ArtistId, req.Msg.MemberId, role, s.artistParticipantAudit(req.Msg.ArtistId, req.Msg.MemberId))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.ArtistParticipant{
		Member: view.Member, Role: req.Msg.Role, CreatedAt: timestamppb.New(view.CreatedAt), HasEffectiveAuthority: view.HasEffectiveAuthority,
	}), nil
}

func (s *ArtistService) RemoveArtistParticipant(
	ctx context.Context,
	req *connect.Request[managev1.RemoveArtistParticipantRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if err := artistEntity.RemoveParticipant(ctx, s.db, s.spiceDB, req.Msg.ArtistId, req.Msg.MemberId, s.artistParticipantAudit(req.Msg.ArtistId, req.Msg.MemberId)); err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

func (s *ArtistService) artistParticipantAudit(artistID, memberID string) entityparticipant.Audit {
	return func(ctx context.Context, tx *gorm.DB, previous, next sharedtelemetry.AuditRelationship) error {
		return s.appendArtistParticipantAudit(ctx, tx, artistID, memberID, previous, next)
	}
}

func artistParticipantFromView(view entityparticipant.View) *managev1.ArtistParticipant {
	role := managev1.ArtistParticipantRole_ARTIST_PARTICIPANT_ROLE_MANAGER
	if view.Role == participantRoleOwner {
		role = managev1.ArtistParticipantRole_ARTIST_PARTICIPANT_ROLE_OWNER
	}
	return &managev1.ArtistParticipant{
		Member: view.Member, Role: role, CreatedAt: timestamppb.New(view.CreatedAt),
		HasEffectiveAuthority: view.HasEffectiveAuthority,
	}
}

func artistParticipantRole(role managev1.ArtistParticipantRole) (string, error) {
	switch role {
	case managev1.ArtistParticipantRole_ARTIST_PARTICIPANT_ROLE_OWNER:
		return participantRoleOwner, nil
	case managev1.ArtistParticipantRole_ARTIST_PARTICIPANT_ROLE_MANAGER:
		return participantRoleManager, nil
	default:
		return "", errs.InvalidArgument("role", "must be Owner or Manager")
	}
}
