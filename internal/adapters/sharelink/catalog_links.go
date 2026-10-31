package sharelinkadapter

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/sharelink"
	"gorm.io/gorm"
)

// CatalogLinks removes the share links of a deleted catalog entity in the
// deleting transaction.
type CatalogLinks struct{}

// NewCatalogLinks returns the share link cleanup of Labels and Artists.
func NewCatalogLinks() CatalogLinks { return CatalogLinks{} }

// DeleteLabelShareLinks deletes every share link of the Label.
func (CatalogLinks) DeleteLabelShareLinks(ctx context.Context, tx *gorm.DB, labelID string) error {
	return EntityLinks{Type: sharelink.EntityLabel}.DeleteEntityShareLinks(ctx, tx, labelID)
}

// DeleteArtistShareLinks deletes every share link of the Artist.
func (CatalogLinks) DeleteArtistShareLinks(ctx context.Context, tx *gorm.DB, artistID string) error {
	return EntityLinks{Type: sharelink.EntityArtist}.DeleteEntityShareLinks(ctx, tx, artistID)
}
