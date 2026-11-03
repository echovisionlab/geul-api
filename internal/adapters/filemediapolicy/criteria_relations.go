package filemediapolicy

import (
	"context"
	"time"

	contentblockpostgres "github.com/echovisionlab/geul-api/internal/contentblock/postgres"
	releasepostgres "github.com/echovisionlab/geul-api/internal/release/postgres"
	"gorm.io/gorm"
)

// CriteriaRelations routes policy relation inserts to the repository that owns them.
type CriteriaRelations struct{}

func (CriteriaRelations) InsertTrack(ctx context.Context, tx *gorm.DB, trackID string, ids []string, now time.Time) error {
	return releasepostgres.InsertTrackDownloadPolicyCriteriaSet(ctx, tx, trackID, ids, now)
}

func (CriteriaRelations) InsertAttachment(ctx context.Context, tx *gorm.DB, blockID, referencePath string, ids []string, now time.Time) error {
	return contentblockpostgres.InsertAttachmentDownloadPolicyCriteriaSet(ctx, tx, blockID, referencePath, ids, now)
}
