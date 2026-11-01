package sharelink

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const (
	maxExpiration     = 365 * 24 * time.Hour
	defaultExpiration = 7 * 24 * time.Hour
)

// ListShareLinksQuery lists the links of one target.
type ListShareLinksQuery struct {
	EntityType EntityType
	EntityID   string
}

// CreateShareLinkCommand creates a link to one target.
type CreateShareLinkCommand struct {
	EntityType EntityType
	EntityID   string
	Label      *string
	// Password, when set, must be presented with the token.
	Password *string
	// ExpiresAt defaults to seven days from now.
	ExpiresAt *time.Time
}

// DeleteShareLinkCommand deletes one link.
type DeleteShareLinkCommand struct {
	ID string
}

func (s *Service) ListShareLinks(ctx context.Context, query ListShareLinksQuery) (_ []Link, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("entity_type", string(query.EntityType)), slog.String("entity_id", query.EntityID))

	if err := validateTarget(query.EntityType, query.EntityID); err != nil {
		return nil, err
	}
	if err := s.authorizeList(ctx, query.EntityType, query.EntityID); err != nil {
		return nil, err
	}
	var links []Link
	if err := s.units.Read(ctx, func(store Store) (err error) {
		links, err = store.ListLinks(ctx, query.EntityType, query.EntityID)
		return err
	}); err != nil {
		return nil, err
	}
	return links, nil
}

// authorizeList requires that the caller may list the links of the target.
// A missing or hidden target answers not found.
func (s *Service) authorizeList(ctx context.Context, entityType EntityType, entityID string) error {
	policy := targetPolicies[entityType]
	var target Target
	if err := s.units.Read(ctx, func(store Store) (err error) {
		target, err = store.Target(ctx, entityType, entityID)
		return err
	}); err != nil {
		if errors.Is(err, ErrTargetNotFound) {
			return domainerr.ResourceNotFound(policy.name, entityID)
		}
		return err
	}
	if err := s.authorizeRead(ctx, policy, entityID, policy.action(target, authorizationRead)); err != nil {
		return err
	}
	return policy.lifecycle(target, time.Now())
}

func (s *Service) CreateShareLink(ctx context.Context, command CreateShareLinkCommand) (_ Link, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("entity_type", string(command.EntityType)), slog.String("entity_id", command.EntityID))

	if err := validateTarget(command.EntityType, command.EntityID); err != nil {
		return Link{}, err
	}
	link, err := newLink(command, dbtime.Now())
	if err != nil {
		return Link{}, err
	}
	var target lockedTarget
	created, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[Link, Store]{
		Access: lockTarget(command.EntityType, command.EntityID, &target),
		Change: func(ctx context.Context, store Store) (Link, error) {
			if target.lifecycle != nil {
				return Link{}, target.lifecycle
			}
			return store.InsertLink(ctx, link)
		},
		Audit: func(created Link) mutation.Audit {
			return target.audit(created.ID, sharedtelemetry.AuditItemOperationCreated)
		},
	})
	if err != nil {
		return Link{}, failurecause.WithPrivate(err, linkSecrets(link)...)
	}
	return created, nil
}

func (s *Service) DeleteShareLink(ctx context.Context, command DeleteShareLinkCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("share_link_id", command.ID))
	var link Link
	if err := s.units.Read(ctx, func(store Store) (err error) {
		link, err = store.Link(ctx, command.ID)
		return err
	}); err != nil {
		if errors.Is(err, ErrLinkNotFound) {
			return domainerr.ResourceNotFound("share link", command.ID)
		}
		return err
	}
	if !link.EntityType.Known() {
		return domainerr.InvalidEntityType(string(link.EntityType))
	}
	var target lockedTarget
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Access: lockTarget(link.EntityType, link.EntityID, &target),
		Change: func(ctx context.Context, store Store) (struct{}, error) {
			if target.lifecycle != nil {
				return struct{}{}, target.lifecycle
			}
			if err := store.DeleteLink(ctx, link); err != nil {
				if errors.Is(err, ErrLinkNotFound) {
					return struct{}{}, domainerr.ResourceNotFound("share link", link.ID)
				}
				return struct{}{}, err
			}
			return struct{}{}, nil
		},
		Audit: func(struct{}) mutation.Audit {
			return target.audit(link.ID, sharedtelemetry.AuditItemOperationDeleted)
		},
	})
	if err != nil {
		return failurecause.WithAttrs(failurecause.WithPrivate(err, linkSecrets(link)...),
			slog.String("entity_type", string(link.EntityType)), slog.String("entity_id", link.EntityID))
	}
	return nil
}

// lockedTarget is a target locked for a link change.
type lockedTarget struct {
	// lifecycle rejects a target whose lifecycle forbids links.
	lifecycle error
	audit     func(linkID string, operation sharedtelemetry.AuditItemOperation) mutation.Audit
}

// lockTarget locks the link's target and requires the action its locked
// lifecycle selects. A caller without it learns nothing about the target:
// the denial answers not found, as a missing target does.
func lockTarget(entityType EntityType, entityID string, locked *lockedTarget) mutation.Access {
	return mutation.Locked(func(ctx context.Context, store Store, require mutation.Require) error {
		policy := targetPolicies[entityType]
		target, err := store.LockTarget(ctx, entityType, entityID)
		if err != nil {
			if errors.Is(err, ErrTargetNotFound) {
				return domainerr.ResourceNotFound(policy.name, entityID)
			}
			return err
		}
		can, err := policy.action(target, authorizationMutation)(entityID)
		if err != nil {
			return domainerr.InvalidField("entity_id", err.Error())
		}
		*locked = lockedTarget{
			lifecycle: policy.lifecycle(target, time.Now()),
			audit: func(linkID string, operation sharedtelemetry.AuditItemOperation) mutation.Audit {
				return linkAudit(entityType, entityID, linkID, target.Version, operation)
			},
		}
		if principal := auth.GetUser(ctx); principal != nil && principal.Authenticated && principal.Banned {
			return domainerr.AccountBanned()
		}
		if err := require(can); err != nil {
			if mutation.IsDenied(err) {
				return domainerr.ResourceNotFound(policy.name, entityID).WithCause(err)
			}
			return err
		}
		return nil
	})
}

func validateTarget(entityType EntityType, entityID string) error {
	if !entityType.Known() {
		return domainerr.InvalidEntityType(string(entityType))
	}
	if _, err := uuid.Parse(entityID); err != nil {
		return domainerr.InvalidEntityID()
	}
	return nil
}

func newLink(command CreateShareLinkCommand, now time.Time) (Link, error) {
	expiresAt, err := expiry(command.ExpiresAt, now)
	if err != nil {
		return Link{}, err
	}
	passwordHash, err := passwordHash(command.Password)
	if err != nil {
		return Link{}, err
	}
	token, err := crypto.GenerateSecureToken()
	if err != nil {
		return Link{}, err
	}
	return Link{
		Token: token, EntityType: command.EntityType, EntityID: command.EntityID, Label: command.Label,
		PasswordHash: passwordHash, CreatedAt: now, ExpiresAt: expiresAt,
	}, nil
}

func expiry(requested *time.Time, now time.Time) (time.Time, error) {
	if requested == nil {
		return now.Add(defaultExpiration), nil
	}
	expiresAt := *requested
	if !expiresAt.After(now) {
		return time.Time{}, domainerr.InvalidField("expires_at", "must be in the future")
	}
	if expiresAt.After(now.Add(maxExpiration)) {
		return time.Time{}, domainerr.InvalidField("expires_at", "cannot exceed 1 year from now")
	}
	return expiresAt, nil
}

func passwordHash(password *string) (*string, error) {
	if password == nil {
		return nil, nil
	}
	if *password == "" {
		return nil, domainerr.InvalidField("password", "must not be empty when provided")
	}
	if len(*password) > 1024 {
		return nil, domainerr.InvalidField("password", "must not exceed 1024 bytes")
	}
	hash, err := crypto.NewPasswordHasher(nil).Hash(*password)
	if err != nil {
		return nil, err
	}
	return &hash, nil
}

// linkSecrets are the stored values of link that no failure log may keep.
func linkSecrets(link Link) []string {
	secrets := []string{link.Token}
	if link.PasswordHash != nil {
		secrets = append(secrets, *link.PasswordHash)
	}
	return secrets
}
