package filemediapolicy

import (
	"context"
	"time"

	contentblockpostgres "github.com/echovisionlab/geul-api/internal/contentblock/postgres"
	releasepostgres "github.com/echovisionlab/geul-api/internal/release/postgres"
	"gorm.io/gorm"
)

// Segments routes policy relation inserts to the repository that owns them.
type Segments struct{}

func (Segments) InsertTrack(ctx context.Context, tx *gorm.DB, trackID string, ids []string, now time.Time) error {
	return releasepostgres.InsertTrackDownloadPolicySegments(ctx, tx, trackID, ids, now)
}

func (Segments) InsertAttachment(ctx context.Context, tx *gorm.DB, blockID, referencePath string, ids []string, now time.Time) error {
	return contentblockpostgres.InsertAttachmentDownloadPolicySegments(ctx, tx, blockID, referencePath, ids, now)
}
