package mediaasset

import (
	"context"

	transcodepostgres "github.com/echovisionlab/geul-api/internal/transcode/postgres"
	"gorm.io/gorm"
)

type ActiveTranscodes struct{ db *gorm.DB }

func NewActiveTranscodes(db *gorm.DB) *ActiveTranscodes {
	if db == nil {
		panic("media asset active transcodes: db is required")
	}
	return &ActiveTranscodes{db: db}
}
func (a *ActiveTranscodes) ActiveFileIDs(ctx context.Context, ids []string) (map[string]struct{}, error) {
	return transcodepostgres.ActiveJobFileIDs(ctx, a.db, ids)
}
