package sharelink

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/crypto"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	maxExpiration     = 365 * 24 * time.Hour
	defaultExpiration = 7 * 24 * time.Hour
)

// CreateRecord and DeleteRecord are persistence continuations invoked by the
// target authority inside its locked transaction. This keeps target lifecycle,
// permission, and Audit ownership outside the generic ShareLink service.
type CreateRecord func(context.Context, *gorm.DB, *model.ShareLink) error
type DeleteRecord func(context.Context, *gorm.DB, model.ShareLink) error

type TargetAuthority interface {
	AuthorizeList(context.Context, managev1.ShareLinkEntityType, string) error
	Create(context.Context, managev1.ShareLinkEntityType, string, *model.ShareLink, CreateRecord) error
	Delete(context.Context, managev1.ShareLinkEntityType, model.ShareLink, DeleteRecord) error
}

type Service struct {
	managev1connect.UnimplementedShareLinkServiceHandler
	db        *gorm.DB
	authority TargetAuthority
}

func NewService(db *gorm.DB, authority TargetAuthority) *Service {
	if db == nil {
		panic("sharelink.Service: db is required")
	}
	if authority == nil {
		panic("sharelink.Service: target authority is required")
	}
	return &Service{db: db, authority: authority}
}

func (s *Service) ListShareLinks(ctx context.Context, req *connect.Request[managev1.ListShareLinksRequest]) (*connect.Response[managev1.ListShareLinksResponse], error) {
	if err := validateTarget(req.Msg.EntityType, req.Msg.EntityId); err != nil {
		return nil, err
	}
	if err := s.authority.AuthorizeList(ctx, req.Msg.EntityType, req.Msg.EntityId); err != nil {
		return nil, err
	}
	var links []model.ShareLink
	if err := s.db.WithContext(ctx).
		Where("entity_type = ? AND entity_id = ?", req.Msg.EntityType.String(), req.Msg.EntityId).
		Order("created_at DESC").Find(&links).Error; err != nil {
		return nil, errs.Internal(err)
	}
	items := make([]*managev1.ShareLinkItem, len(links))
	for i := range links {
		items[i] = toProto(&links[i])
	}
	return connect.NewResponse(&managev1.ListShareLinksResponse{ShareLinks: items}), nil
}

func (s *Service) CreateShareLink(ctx context.Context, req *connect.Request[managev1.CreateShareLinkRequest]) (*connect.Response[managev1.CreateShareLinkResponse], error) {
	if err := validateTarget(req.Msg.EntityType, req.Msg.EntityId); err != nil {
		return nil, err
	}
	link, err := build(req.Msg, time.Now())
	if err != nil {
		return nil, err
	}
	if err := s.authority.Create(ctx, req.Msg.EntityType, req.Msg.EntityId, link, createRecord); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.CreateShareLinkResponse{ShareLink: toProto(link)}), nil
}

func (s *Service) DeleteShareLink(ctx context.Context, req *connect.Request[managev1.DeleteShareLinkRequest]) (*connect.Response[managev1.DeleteShareLinkResponse], error) {
	var link model.ShareLink
	if err := s.db.WithContext(ctx).First(&link, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("share link", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}
	value, ok := managev1.ShareLinkEntityType_value[link.EntityType]
	if !ok || value == int32(managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_UNSPECIFIED) {
		return nil, errs.InvalidEntityType(link.EntityType)
	}
	if err := s.authority.Delete(ctx, managev1.ShareLinkEntityType(value), link, deleteRecord); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.DeleteShareLinkResponse{Success: true}), nil
}

func validateTarget(entityType managev1.ShareLinkEntityType, entityID string) error {
	if entityType == managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_UNSPECIFIED {
		return errs.InvalidEntityType(entityType.String())
	}
	if _, err := uuid.Parse(entityID); err != nil {
		return errs.InvalidEntityID()
	}
	return nil
}

func build(request *managev1.CreateShareLinkRequest, now time.Time) (*model.ShareLink, error) {
	expiresAt, err := expiry(request, now)
	if err != nil {
		return nil, err
	}
	passwordHash, err := passwordHash(request)
	if err != nil {
		return nil, err
	}
	token, err := crypto.GenerateSecureToken()
	if err != nil {
		return nil, errs.Internal(err)
	}
	return &model.ShareLink{Token: token, EntityType: request.EntityType.String(), EntityID: request.EntityId, Label: request.Label, PasswordHash: passwordHash, CreatedAt: now, ExpiresAt: &expiresAt}, nil
}

func expiry(request *managev1.CreateShareLinkRequest, now time.Time) (time.Time, error) {
	if request.ExpiresAt == nil {
		return now.Add(defaultExpiration), nil
	}
	expiresAt := request.ExpiresAt.AsTime()
	if !expiresAt.After(now) {
		return time.Time{}, errs.InvalidArgument("expires_at", "must be in the future")
	}
	if expiresAt.After(now.Add(maxExpiration)) {
		return time.Time{}, errs.InvalidArgument("expires_at", "cannot exceed 1 year from now")
	}
	return expiresAt, nil
}

func passwordHash(request *managev1.CreateShareLinkRequest) (*string, error) {
	if request.Password == nil {
		return nil, nil
	}
	password := request.GetPassword()
	if password == "" {
		return nil, errs.InvalidArgument("password", "must not be empty when provided")
	}
	if len(password) > 1024 {
		return nil, errs.InvalidArgument("password", "must not exceed 1024 bytes")
	}
	hash, err := crypto.NewPasswordHasher(nil).Hash(password)
	if err != nil {
		return nil, errs.Internal(err)
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
		return errs.NotFound("share link", link.ID)
	}
	return nil
}

func toProto(link *model.ShareLink) *managev1.ShareLinkItem {
	item := &managev1.ShareLinkItem{Id: link.ID, EntityType: managev1.ShareLinkEntityType(managev1.ShareLinkEntityType_value[link.EntityType]), EntityId: link.EntityID, Token: link.Token, Url: fmt.Sprintf("/s/%s", link.Token), CreatedAt: timestamppb.New(link.CreatedAt), HasPassword: link.PasswordHash != nil}
	if link.Label != nil {
		item.Label = link.Label
	}
	if link.ExpiresAt != nil {
		item.ExpiresAt = timestamppb.New(*link.ExpiresAt)
	}
	return item
}
