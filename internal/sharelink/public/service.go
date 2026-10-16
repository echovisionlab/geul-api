package public

import (
	"context"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/crypto"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// ShareLinkService implements the public ShareLinkService
type ShareLinkService struct {
	openv1connect.UnimplementedShareLinkServiceHandler
	db      *gorm.DB
	targets TargetResolver
}

type Target struct {
	Slug          *string
	Exists        bool
	PublicHistory bool
}

type TargetResolver interface {
	Resolve(context.Context, managev1.ShareLinkEntityType, string, time.Time) (Target, error)
	IsAutomaticPublicHistoryToken(context.Context, string, managev1.ShareLinkEntityType, string) (bool, error)
}

func NewService(db *gorm.DB, targets TargetResolver) *ShareLinkService {
	if db == nil {
		panic("sharelink public service: db is required")
	}
	if targets == nil {
		panic("sharelink public service: target resolver is required")
	}
	return &ShareLinkService{db: db, targets: targets}
}

// Validate validates a share link token and returns entity info if valid
func (s *ShareLinkService) Validate(
	ctx context.Context,
	req *connect.Request[openv1.ValidateShareLinkRequest],
) (_ *connect.Response[openv1.ValidateShareLinkResponse], returnErr error) {
	var link model.ShareLink
	defer func() {
		if returnErr == nil || req == nil {
			return
		}
		level := slog.LevelWarn
		switch connect.CodeOf(returnErr) {
		case connect.CodeUnknown, connect.CodeInternal, connect.CodeUnavailable, connect.CodeDataLoss, connect.CodeDeadlineExceeded:
			level = slog.LevelError
		}
		private := []string{req.Msg.GetToken(), req.Msg.GetPassword()}
		if link.PasswordHash != nil {
			private = append(private, *link.PasswordHash)
		}
		slog.Log(ctx, level, "content operation failed", "domain", "sharelink", "operation", "sharelink/public.Validate", "outcome", "failed", "entity_type", link.EntityType, "entity_id", link.EntityID, failurecause.Attr(returnErr, private...))
	}()

	token := req.Msg.Token
	if token == "" {
		return connect.NewResponse(&openv1.ValidateShareLinkResponse{Valid: false}), nil
	}

	if err := s.db.WithContext(ctx).First(&link, "token = ?", token).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			slog.WarnContext(ctx, "content operation failed", "domain", "sharelink", "operation", "validate_share_link", "outcome", "failed", failurecause.Attr(err), "reason", "not_found")
			return connect.NewResponse(&openv1.ValidateShareLinkResponse{Valid: false}), nil
		}
		return nil, errs.Internal(err)
	}

	now := time.Now()
	entityTypeValue, ok := managev1.ShareLinkEntityType_value[link.EntityType]
	if !ok {
		return connect.NewResponse(&openv1.ValidateShareLinkResponse{Valid: false}), nil
	}
	entityType := managev1.ShareLinkEntityType(entityTypeValue)
	target, err := s.targets.Resolve(ctx, entityType, link.EntityID, now)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if !target.Exists {
		return connect.NewResponse(&openv1.ValidateShareLinkResponse{Valid: false}), nil
	}
	// Expired automatic legal preview tokens never retain private authority.
	// Only a token sealed into the exact scheduled update notice may resolve the
	// now-public version. Manual legal ShareLinks keep the normal expiry and
	// password contract.
	if link.ExpiresAt == nil || !link.ExpiresAt.After(now) {
		if link.ExpiresAt == nil || !target.PublicHistory || link.PasswordHash != nil {
			return connect.NewResponse(&openv1.ValidateShareLinkResponse{Valid: false}), nil
		}
		automatic, err := s.targets.IsAutomaticPublicHistoryToken(
			ctx,
			token,
			entityType,
			link.EntityID,
		)
		if err != nil {
			return nil, errs.Internal(err)
		}
		if !automatic {
			return connect.NewResponse(&openv1.ValidateShareLinkResponse{Valid: false}), nil
		}
		return connect.NewResponse(&openv1.ValidateShareLinkResponse{
			Valid:            true,
			EntityType:       &entityType,
			EntityId:         &link.EntityID,
			Slug:             target.Slug,
			PasswordRequired: false,
		}), nil
	}
	if link.PasswordHash != nil {
		response := &openv1.ValidateShareLinkResponse{
			Valid:            false,
			EntityType:       &entityType,
			EntityId:         &link.EntityID,
			Slug:             target.Slug,
			PasswordRequired: true,
		}
		if req.Msg.Password == nil || req.Msg.GetPassword() == "" {
			return connect.NewResponse(response), nil
		}
		match, err := crypto.NewPasswordHasher(nil).Verify(req.Msg.GetPassword(), *link.PasswordHash)
		if err != nil {
			return nil, errs.Internal(err)
		}
		if !match {
			return connect.NewResponse(response), nil
		}
	}

	return connect.NewResponse(&openv1.ValidateShareLinkResponse{
		Valid:            true,
		EntityType:       &entityType,
		EntityId:         &link.EntityID,
		Slug:             target.Slug,
		PasswordRequired: link.PasswordHash != nil,
	}), nil
}
