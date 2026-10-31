package sharelink

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
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

func (s *Service) ListShareLinks(ctx context.Context, query ListShareLinksQuery) (_ []*managev1.ShareLinkItem, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("entity_type", string(query.EntityType)), slog.String("entity_id", query.EntityID))

	if err := validateTarget(query.EntityType, query.EntityID); err != nil {
		return nil, err
	}
	if err := s.targets.AuthorizeList(ctx, query.EntityType, query.EntityID); err != nil {
		return nil, err
	}
	var links []model.ShareLink
	if err := s.db.WithContext(ctx).
		Where("entity_type = ? AND entity_id = ?", string(query.EntityType), query.EntityID).
		Order("created_at DESC").Find(&links).Error; err != nil {
		return nil, err
	}
	items := make([]*managev1.ShareLinkItem, len(links))
	for i := range links {
		items[i] = toProto(&links[i])
	}
	return items, nil
}

func (s *Service) CreateShareLink(ctx context.Context, command CreateShareLinkCommand) (_ *managev1.ShareLinkItem, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("entity_type", string(command.EntityType)), slog.String("entity_id", command.EntityID))

	if err := validateTarget(command.EntityType, command.EntityID); err != nil {
		return nil, err
	}
	link, err := build(command, dbtime.Now())
	if err != nil {
		return nil, err
	}
	var target LockedTarget
	created, err := mutation.Run(ctx, s.mutations, mutation.Spec[*model.ShareLink]{
		Access: s.lockedTarget(command.EntityType, command.EntityID, &target),
		Change: func(ctx context.Context, tx *gorm.DB) (*model.ShareLink, error) {
			if target.Lifecycle != nil {
				return nil, target.Lifecycle
			}
			return link, createRecord(ctx, tx, link)
		},
		Audit: func(link *model.ShareLink) mutation.Audit {
			return target.Audit(link.ID, sharedtelemetry.AuditItemOperationCreated)
		},
	})
	if err != nil {
		return nil, failurecause.WithPrivate(err, linkSecrets(link)...)
	}
	return toProto(created), nil
}

func (s *Service) DeleteShareLink(ctx context.Context, command DeleteShareLinkCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("share_link_id", command.ID))
	var link model.ShareLink
	if err := s.db.WithContext(ctx).First(&link, "id = ?", command.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("share link", command.ID)
		}
		return err
	}
	entityType := EntityType(link.EntityType)
	if !entityType.known() {
		return domainerr.InvalidEntityType(link.EntityType)
	}
	var target LockedTarget
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		Access: s.lockedTarget(entityType, link.EntityID, &target),
		Change: func(ctx context.Context, tx *gorm.DB) (struct{}, error) {
			if target.Lifecycle != nil {
				return struct{}{}, target.Lifecycle
			}
			return struct{}{}, deleteRecord(ctx, tx, link)
		},
		Audit: func(struct{}) mutation.Audit {
			return target.Audit(link.ID, sharedtelemetry.AuditItemOperationDeleted)
		},
	})
	if err != nil {
		return failurecause.WithAttrs(failurecause.WithPrivate(err, linkSecrets(&link)...),
			slog.String("entity_type", link.EntityType), slog.String("entity_id", link.EntityID))
	}
	return nil
}

// lockedTarget locks the link's target and requires the action its locked
// lifecycle selects. A caller without it learns nothing about the target:
// the denial answers not found, as a missing target does.
func (s *Service) lockedTarget(entityType EntityType, entityID string, target *LockedTarget) mutation.Access {
	return mutation.Locked(func(ctx context.Context, tx *gorm.DB, require mutation.Require) error {
		locked, err := s.targets.LockTarget(ctx, tx, entityType, entityID)
		if err != nil {
			return err
		}
		*target = locked
		if principal := auth.GetUser(ctx); principal != nil && principal.Authenticated && principal.Banned {
			return domainerr.AccountBanned()
		}
		if err := require(locked.Can); err != nil {
			if mutation.IsDenied(err) {
				return domainerr.ResourceNotFound(locked.Name, entityID).WithCause(err)
			}
			return err
		}
		return nil
	})
}

func validateTarget(entityType EntityType, entityID string) error {
	if !entityType.known() {
		return domainerr.InvalidEntityType(string(entityType))
	}
	if _, err := uuid.Parse(entityID); err != nil {
		return domainerr.InvalidEntityID()
	}
	return nil
}

func build(command CreateShareLinkCommand, now time.Time) (*model.ShareLink, error) {
	expiresAt, err := expiry(command.ExpiresAt, now)
	if err != nil {
		return nil, err
	}
	passwordHash, err := passwordHash(command.Password)
	if err != nil {
		return nil, err
	}
	token, err := crypto.GenerateSecureToken()
	if err != nil {
		return nil, err
	}
	return &model.ShareLink{Token: token, EntityType: string(command.EntityType), EntityID: command.EntityID, Label: command.Label, PasswordHash: passwordHash, CreatedAt: now, ExpiresAt: &expiresAt}, nil
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

func createRecord(ctx context.Context, tx *gorm.DB, link *model.ShareLink) error {
	return tx.WithContext(ctx).Clauses(clause.Returning{}).
		Select("Token", "EntityType", "EntityID", "Label", "PasswordHash", "ExpiresAt", "CreatedAt").Create(link).Error
}

func deleteRecord(ctx context.Context, tx *gorm.DB, link model.ShareLink) error {
	result := tx.WithContext(ctx).Where("id = ? AND entity_type = ? AND entity_id = ?", link.ID, link.EntityType, link.EntityID).Delete(&model.ShareLink{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return domainerr.ResourceNotFound("share link", link.ID)
	}
	return nil
}

func toProto(link *model.ShareLink) *managev1.ShareLinkItem {
	item := &managev1.ShareLinkItem{Id: link.ID, EntityType: EntityTypeToProto(EntityType(link.EntityType)), EntityId: link.EntityID, Token: link.Token, Url: fmt.Sprintf("/s/%s", link.Token), CreatedAt: timestamppb.New(link.CreatedAt), HasPassword: link.PasswordHash != nil}
	if link.Label != nil {
		item.Label = link.Label
	}
	if link.ExpiresAt != nil {
		item.ExpiresAt = timestamppb.New(*link.ExpiresAt)
	}
	return item
}

// linkSecrets are the stored values of link that no failure log may keep.
func linkSecrets(link *model.ShareLink) []string {
	secrets := []string{link.Token}
	if link.PasswordHash != nil {
		secrets = append(secrets, *link.PasswordHash)
	}
	return secrets
}
