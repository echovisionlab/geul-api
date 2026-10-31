package public

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// Service holds the public Share Link use case.
type Service struct {
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

// Deps are the collaborators of the public Share Link use case. Both are
// required.
type Deps struct {
	DB      *gorm.DB
	Targets TargetResolver
}

// NewService validates deps and returns the public Share Link use case.
func NewService(deps Deps) (*Service, error) {
	if deps.DB == nil {
		return nil, errors.New("public share link database is required")
	}
	if deps.Targets == nil {
		return nil, errors.New("public share link target resolver is required")
	}
	return &Service{db: deps.DB, targets: deps.Targets}, nil
}

// ValidateShareLinkQuery presents a token and its optional password.
type ValidateShareLinkQuery struct {
	Token    string
	Password *string
}

// Validate validates a share link token and returns entity info if valid
func (s *Service) ValidateShareLink(ctx context.Context, query ValidateShareLinkQuery) (*openv1.ValidateShareLinkResponse, error) {
	var link model.ShareLink
	token := query.Token
	if token == "" {
		return &openv1.ValidateShareLinkResponse{Valid: false}, nil
	}

	if err := s.db.WithContext(ctx).First(&link, "token = ?", token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &openv1.ValidateShareLinkResponse{Valid: false}, nil
		}
		return nil, err
	}

	now := time.Now()
	entityType := sharelink.EntityTypeToProto(sharelink.EntityType(link.EntityType))
	if entityType == managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_UNSPECIFIED {
		return &openv1.ValidateShareLinkResponse{Valid: false}, nil
	}
	target, err := s.targets.Resolve(ctx, entityType, link.EntityID, now)
	if err != nil {
		return nil, err
	}
	if !target.Exists {
		return &openv1.ValidateShareLinkResponse{Valid: false}, nil
	}
	// Expired automatic legal preview tokens never retain private authority.
	// Only a token sealed into the exact scheduled update notice may resolve the
	// now-public version. Manual legal ShareLinks keep the normal expiry and
	// password contract.
	if link.ExpiresAt == nil || !link.ExpiresAt.After(now) {
		if link.ExpiresAt == nil || !target.PublicHistory || link.PasswordHash != nil {
			return &openv1.ValidateShareLinkResponse{Valid: false}, nil
		}
		automatic, err := s.targets.IsAutomaticPublicHistoryToken(
			ctx,
			token,
			entityType,
			link.EntityID,
		)
		if err != nil {
			return nil, err
		}
		if !automatic {
			return &openv1.ValidateShareLinkResponse{Valid: false}, nil
		}
		return &openv1.ValidateShareLinkResponse{
			Valid:            true,
			EntityType:       &entityType,
			EntityId:         &link.EntityID,
			Slug:             target.Slug,
			PasswordRequired: false,
		}, nil
	}
	if link.PasswordHash != nil {
		response := &openv1.ValidateShareLinkResponse{
			Valid:            false,
			EntityType:       &entityType,
			EntityId:         &link.EntityID,
			Slug:             target.Slug,
			PasswordRequired: true,
		}
		if query.Password == nil || *query.Password == "" {
			return response, nil
		}
		match, err := crypto.NewPasswordHasher(nil).Verify(*query.Password, *link.PasswordHash)
		if err != nil {
			return nil, failurecause.WithPrivate(err, *link.PasswordHash)
		}
		if !match {
			return response, nil
		}
	}

	return &openv1.ValidateShareLinkResponse{
		Valid:            true,
		EntityType:       &entityType,
		EntityId:         &link.EntityID,
		Slug:             target.Slug,
		PasswordRequired: link.PasswordHash != nil,
	}, nil
}
