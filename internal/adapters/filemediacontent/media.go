package filemediacontent

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	filemediapostgres "github.com/echovisionlab/geul-api/internal/filemedia/postgres"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Media struct{ service *filemedia.Service }

func New(service *filemedia.Service) *Media { return &Media{service: service} }
func (m *Media) DeleteFileByID(ctx context.Context, id string) error {
	return m.service.DeleteFileByID(ctx, id)
}
func joined(tx *gorm.DB) filemedia.Joined {
	if tx == nil {
		return filemedia.Joined{}
	}
	return filemediapostgres.Bind(tx, filemediapostgres.Ports{}, nil)
}
func (m *Media) HydrateAuthorizedPageBlockMediaWithDB(ctx context.Context, tx *gorm.DB, pageID string, documentID uuid.UUID, principal *auth.UserInfo, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
	return m.service.HydrateAuthorizedPageBlockMedia(ctx, joined(tx), pageID, documentID, principal, items)
}
func (m *Media) HydrateAuthorizedPostBlockMediaWithDB(ctx context.Context, tx *gorm.DB, postID string, documentID uuid.UUID, principal *auth.UserInfo, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
	return m.service.HydrateAuthorizedPostBlockMedia(ctx, joined(tx), postID, documentID, principal, items)
}
func (m *Media) HydrateAuthorizedWorkBlockMediaWithDB(ctx context.Context, tx *gorm.DB, workID string, documentID uuid.UUID, principal *auth.UserInfo, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
	return m.service.HydrateAuthorizedWorkBlockMedia(ctx, joined(tx), workID, documentID, principal, items)
}
func (m *Media) HydrateAuthorizedProgramEventBlockMediaWithDB(ctx context.Context, tx *gorm.DB, eventID string, documentID uuid.UUID, principal *auth.UserInfo, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
	return m.service.HydrateAuthorizedProgramEventBlockMedia(ctx, joined(tx), eventID, documentID, principal, items)
}
