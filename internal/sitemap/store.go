// Package sitemap owns the crawler sitemap read model and persistence ports.
package sitemap

import (
	"context"
	"time"
)

// SiteContext contains the site-owned values needed to render sitemap URLs.
type SiteContext struct {
	HomepagePageID *string
}

// Entry is one domain-neutral row in the sitemap read model.
type Entry struct {
	ID          string
	Slug        *string
	PublishedAt *time.Time
	UpdatedAt   *time.Time
	CreatedAt   *time.Time
}

// Snapshot is the last successfully materialized sitemap document.
type Snapshot struct {
	Content     string
	ContentType string
	GeneratedAt time.Time
}

// ReadModel exposes the cross-domain rows required to render public sitemaps.
type ReadModel interface {
	LoadSiteContext(context.Context) (SiteContext, error)
	LoadHomepage(context.Context, string) (*Entry, error)
	ListPages(context.Context) ([]Entry, error)
	ListPrivacyHistory(context.Context) ([]Entry, error)
	ListTermsHistory(context.Context) ([]Entry, error)
	ListPosts(context.Context) ([]Entry, error)
	ListWorks(context.Context) ([]Entry, error)
	ListCategories(context.Context) ([]Entry, error)
	ListTags(context.Context) ([]Entry, error)
}

// SnapshotStore persists the last successfully materialized sitemap document.
type SnapshotStore interface {
	LoadSnapshot(context.Context, string) (*Snapshot, error)
	// SaveSnapshot reports whether the snapshot row was inserted or materially updated.
	SaveSnapshot(context.Context, string, *Snapshot) (bool, error)
}

// Store is the complete persistence port required by the public Sitemap service.
type Store interface {
	ReadModel
	SnapshotStore
}
