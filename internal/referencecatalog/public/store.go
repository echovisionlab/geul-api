package public

import (
	"context"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
)

// Category is a category with its published and archived Post count.
type Category struct {
	ID          string
	Name        string
	Slug        string
	Description *string
	PostCount   int32
}

// Tag is a tag with its published and archived Post count.
type Tag struct {
	ID        string
	Name      string
	Slug      string
	PostCount int32
}

// Store is what the public catalog reads read, bound to one read snapshot.
// referencecatalogpublicpostgres implements it. A missing Client is
// referencecatalog.ErrClientNotFound.
type Store interface {
	// Categories and Tags list one page with their Post counts and count
	// every match.
	Categories(ctx context.Context, page referencecatalog.ListPage) ([]Category, int64, error)
	Tags(ctx context.Context, page referencecatalog.ListPage) ([]Tag, int64, error)
	// Client reads one Client.
	Client(ctx context.Context, id string) (referencecatalog.Client, error)
	// Clients lists one page of Clients and counts every match.
	Clients(ctx context.Context, page referencecatalog.ListPage) ([]referencecatalog.Client, int64, error)
	// MapPlacesByID reads those of ids that exist, in no particular order.
	MapPlacesByID(ctx context.Context, ids []string) ([]referencecatalog.MapPlace, error)
	// ReadyAsset resolves the ready public asset of a source file, or nil
	// when there is none or it cannot be served.
	ReadyAsset(ctx context.Context, source referencecatalog.AssetSource) *mediaasset.Reference
}
