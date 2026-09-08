package release

import (
	"context"
	"strings"
	"time"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	releasepublic "github.com/echovisionlab/geul-api/internal/release/public"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"gorm.io/gorm"
)

type PublicMedia struct {
	cdnDomain   string
	mediaDomain string
	mediaSecret string
	downloadTTL time.Duration
}

func NewPublicMedia(
	cdnDomain string,
	mediaDomain string,
	mediaSecret string,
	downloadTTL time.Duration,
) *PublicMedia {
	if downloadTTL <= 0 || downloadTTL > mediaauth.DownloadTTL {
		downloadTTL = mediaauth.DownloadTTL
	}
	return &PublicMedia{
		cdnDomain: strings.TrimSpace(cdnDomain), mediaDomain: strings.TrimSpace(mediaDomain),
		mediaSecret: mediaSecret, downloadTTL: downloadTTL,
	}
}

func (m *PublicMedia) ReadySourceAssets(
	ctx context.Context,
	db *gorm.DB,
	fileIDs []string,
	kinds ...string,
) (map[string]*commonv1.AssetRef, error) {
	rows, err := loadReadyPublicAssetsForSourceFiles(ctx, db, fileIDs, kinds...)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*commonv1.AssetRef, len(rows))
	for fileID, row := range rows {
		ref, err := projectReadyPublicAsset(m.cdnDomain, row)
		if err != nil {
			return nil, err
		}
		result[fileID] = ref
	}
	return result, nil
}

func (m *PublicMedia) TrackWaveforms(
	ctx context.Context,
	db *gorm.DB,
	fileIDs []string,
) (map[string]*commonv1.AssetRef, error) {
	return loadTrackWaveformRefs(ctx, db, m.mediaDomain, fileIDs)
}

func (m *PublicMedia) TrackSpectrograms(
	ctx context.Context,
	db *gorm.DB,
	fileIDs []string,
) (map[string]*commonv1.AssetRef, error) {
	return loadTrackSpectrogramRefs(ctx, db, m.mediaDomain, fileIDs)
}

func (m *PublicMedia) TrackHLS(
	ctx context.Context,
	db *gorm.DB,
	fileIDs []string,
) (map[string]*commonv1.HlsMediaRef, error) {
	return loadTrackHLSRefs(ctx, db, m.mediaDomain, fileIDs)
}

func (m *PublicMedia) DownloadRef(file releasepublic.MediaFile) (*commonv1.ExpiringMediaRef, error) {
	return buildExpiringFileRef(
		m.mediaDomain, m.mediaSecret, file.ID, file.Extension, file.MimeType,
		file.FileName, mediaauth.PurposeDownload, m.downloadTTL,
	)
}

var _ releasepublic.MediaRuntime = (*PublicMedia)(nil)
