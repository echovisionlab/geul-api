package artist

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

// ArtistService implements the ArtistService Connect handler
func (s *ArtistService) ListArtistParticipants(
	ctx context.Context,
	req *connect.Request[managev1.ListArtistParticipantsRequest],
) (_ *connect.Response[managev1.ListArtistParticipantsResponse], terminalErr error) {
	defer func() {
		if terminalErr != nil {
			level := errs.OperationLogLevel(terminalErr)
			slog.Log(ctx, level, "Operation failed", "domain", "artist", "operation", "ListArtistParticipants", failurecause.Attr(terminalErr), "code", connect.CodeOf(terminalErr).String())
		}
	}()

	if err := requireArtistPermission(ctx, s.spiceDB, req.Msg.ArtistId, policyv1.Artist.ManageParticipants); err != nil {
		return nil, err
	}
	views, err := loadEntityParticipantViews(ctx, s.db, s.spiceDB, s.members, artistParticipantSpec(), req.Msg.ArtistId)
	if err != nil {
		return nil, err
	}
	participants := mapEntityParticipantViews(views, artistParticipantFromView)
	return connect.NewResponse(&managev1.ListArtistParticipantsResponse{Participants: participants}), nil
}

func (s *ArtistService) SetArtistParticipant(
	ctx context.Context,
	req *connect.Request[managev1.SetArtistParticipantRequest],
) (_ *connect.Response[managev1.ArtistParticipant], terminalErr error) {
	defer func() {
		if terminalErr != nil {
			level := errs.OperationLogLevel(terminalErr)
			slog.Log(ctx, level, "Operation failed", "domain", "artist", "operation", "SetArtistParticipant", failurecause.Attr(terminalErr), "code", connect.CodeOf(terminalErr).String())
		}
	}()

	role, err := artistParticipantRole(req.Msg.Role)
	if err != nil {
		return nil, err
	}
	view, err := setEntityParticipantViewWithAudit(ctx, s.db, s.spiceDB, s.members, artistParticipantSpec(), req.Msg.ArtistId, req.Msg.MemberId, role, func(ctx context.Context, tx *gorm.DB, previous, next string) error {
		return s.appendArtistParticipantAudit(ctx, tx, req.Msg.ArtistId, req.Msg.MemberId, artistAuditRelationship(previous), artistAuditRelationship(next))
	})
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
) (_ *connect.Response[managev1.DeleteResponse], terminalErr error) {
	defer func() {
		if terminalErr != nil {
			level := errs.OperationLogLevel(terminalErr)
			slog.Log(ctx, level, "Operation failed", "domain", "artist", "operation", "RemoveArtistParticipant", failurecause.Attr(terminalErr), "code", connect.CodeOf(terminalErr).String())
		}
	}()

	return removeEntityParticipantResponseWithAudit(ctx, s.db, s.spiceDB, artistParticipantSpec(), req.Msg.ArtistId, req.Msg.MemberId, func(ctx context.Context, tx *gorm.DB, previous, next string) error {
		return s.appendArtistParticipantAudit(ctx, tx, req.Msg.ArtistId, req.Msg.MemberId, artistAuditRelationship(previous), artistAuditRelationship(next))
	})
}

func artistAuditRelationship(role string) sharedtelemetry.AuditRelationship {
	if role == participantRoleOwner {
		return sharedtelemetry.AuditRelationshipOwner
	}
	if role == participantRoleManager {
		return sharedtelemetry.AuditRelationshipManager
	}
	return sharedtelemetry.AuditRelationshipNone
}

func artistParticipantFromView(view entityParticipantView) *managev1.ArtistParticipant {
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

// ==================== My Artists ====================
