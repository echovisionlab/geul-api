package public

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/sharelink"
)

// Service holds the public Share Link use case.
type Service struct {
	units   mutation.UnitOfWork[sharelink.Store]
	targets TargetResolver
}

// Target is what a visitor may learn of a link's target.
type Target struct {
	Slug   *string
	Exists bool
	// PublicHistory reports a legal version that is or was in effect, which
	// an expired automatic preview link may still open.
	PublicHistory bool
}

// TargetResolver reads the targets other domains own.
type TargetResolver interface {
	Resolve(ctx context.Context, entityType sharelink.EntityType, entityID string, now time.Time) (Target, error)
	// IsAutomaticPublicHistoryToken reports whether token is the preview
	// link a legal update notice mailed out for the version.
	IsAutomaticPublicHistoryToken(ctx context.Context, token string, entityType sharelink.EntityType, entityID string) (bool, error)
}

// Deps are the collaborators of the public Share Link use case. Both are
// required.
type Deps struct {
	Units   mutation.UnitOfWork[sharelink.Store] // sharelinkpostgres.NewUnits(db)
	Targets TargetResolver
}

// NewService validates deps and returns the public Share Link use case.
func NewService(deps Deps) (*Service, error) {
	if deps.Units == nil {
		return nil, errors.New("public share link units are required")
	}
	if deps.Targets == nil {
		return nil, errors.New("public share link target resolver is required")
	}
	return &Service{units: deps.Units, targets: deps.Targets}, nil
}

// ValidateShareLinkQuery presents a token and its optional password.
type ValidateShareLinkQuery struct {
	Token    string
	Password *string
}

// Validation is what a presented token opens.
type Validation struct {
	Valid bool
	// PasswordRequired reports a link that needs a password, which a
	// visitor learns with its target before presenting the password.
	PasswordRequired bool
	// Target is the linked target, nil when the token opens none.
	Target *ValidatedTarget
}

// ValidatedTarget is the target a token names.
type ValidatedTarget struct {
	EntityType sharelink.EntityType
	EntityID   string
	Slug       *string
}

// ValidateShareLink validates a share link token and returns its target
// when the token opens it.
func (s *Service) ValidateShareLink(ctx context.Context, query ValidateShareLinkQuery) (Validation, error) {
	token := query.Token
	if token == "" {
		return Validation{}, nil
	}
	var link sharelink.Link
	if err := s.units.Read(ctx, func(store sharelink.Store) (err error) {
		link, err = store.LinkByToken(ctx, token)
		return err
	}); err != nil {
		if errors.Is(err, sharelink.ErrLinkNotFound) {
			return Validation{}, nil
		}
		return Validation{}, err
	}

	now := time.Now()
	if !link.EntityType.Known() {
		return Validation{}, nil
	}
	target, err := s.targets.Resolve(ctx, link.EntityType, link.EntityID, now)
	if err != nil {
		return Validation{}, err
	}
	if !target.Exists {
		return Validation{}, nil
	}
	validated := &ValidatedTarget{EntityType: link.EntityType, EntityID: link.EntityID, Slug: target.Slug}
	// Expired automatic legal preview tokens never retain private authority.
	// Only a token sealed into the exact scheduled update notice may resolve the
	// now-public version. Manual legal ShareLinks keep the normal expiry and
	// password contract.
	if !link.ExpiresAt.After(now) {
		if !target.PublicHistory || link.PasswordHash != nil {
			return Validation{}, nil
		}
		automatic, err := s.targets.IsAutomaticPublicHistoryToken(ctx, token, link.EntityType, link.EntityID)
		if err != nil {
			return Validation{}, err
		}
		if !automatic {
			return Validation{}, nil
		}
		return Validation{Valid: true, Target: validated}, nil
	}
	if link.PasswordHash != nil {
		locked := Validation{PasswordRequired: true, Target: validated}
		if query.Password == nil || *query.Password == "" {
			return locked, nil
		}
		match, err := crypto.NewPasswordHasher(nil).Verify(*query.Password, *link.PasswordHash)
		if err != nil {
			return Validation{}, failurecause.WithPrivate(err, *link.PasswordHash)
		}
		if !match {
			return locked, nil
		}
	}
	return Validation{Valid: true, PasswordRequired: link.PasswordHash != nil, Target: validated}, nil
}
