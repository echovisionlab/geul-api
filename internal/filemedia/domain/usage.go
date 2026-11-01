package domain

import "context"

type FileUsageRow struct {
	FileID        string
	Domain        string
	EntityID      string
	ReferencePath string
	BlockID       *string
	BlockType     *string
	Title         *string
	Link          *string
}

type FileUsageSlug struct {
	Domain, EntityID string
	Slug             *string
}
type UsageStore interface {
	FileUsages(context.Context, []string) ([]FileUsageRow, error)
	FileUsageSlugs(context.Context, []string) ([]FileUsageSlug, error)
	TrackReleaseIDs(context.Context, []string) (map[string]string, error)
	ActiveFileCount(context.Context, string) (int64, error)
}
