package public

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

type DownloadAccessResolver interface {
	Resolve(
		context.Context,
		string,
		string,
		mediaasset.ContentDownloadOwnerAuthorization,
		[]TrackDownloadAccessRequest,
		TrackDownloadSigner,
	) (map[string]TrackDownloadAuthorization, error)
}

type TrackDownloadSigner func(MediaFile) (*commonv1.ExpiringMediaRef, error)

type TrackDownloadAuthorization struct {
	Access   *openv1.FileDownloadAccess
	Download *commonv1.ExpiringMediaRef
}

type TrackDownloadAccessRequest struct {
	TrackID string
	FileID  string
}

// MediaRuntime owns Release public media persistence, CDN projection and
// signed delivery URL construction. The public domain service supplies only
// release-owned identities and applies response policy.
type MediaRuntime interface {
	ReadySourceAssets(context.Context, *gorm.DB, []string, ...string) (map[string]*commonv1.AssetRef, error)
	TrackWaveforms(context.Context, *gorm.DB, []string) (map[string]*commonv1.AssetRef, error)
	TrackSpectrograms(context.Context, *gorm.DB, []string) (map[string]*commonv1.AssetRef, error)
	TrackHLS(context.Context, *gorm.DB, []string) (map[string]*commonv1.HlsMediaRef, error)
	DownloadRef(MediaFile) (*commonv1.ExpiringMediaRef, error)
}

type MediaFile struct {
	ID        string
	Extension string
	MimeType  string
	FileName  *string
}

type scopedMediaFile struct {
	ID        string  `gorm:"column:id"`
	Extension string  `gorm:"column:extension"`
	MimeType  string  `gorm:"column:mime_type"`
	FileSize  int64   `gorm:"column:file_size"`
	FileName  *string `gorm:"column:file_name"`
}

func hasDraftReleaseView(ctx context.Context, spiceDB *auth.SpiceDBClient, releaseID string) (bool, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.Banned {
		return false, nil
	}
	can, err := policyv1.Release.View(releaseID)
	if err != nil {
		return false, err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return false, err
	}
	return spiceDB.Can(ctx, decision)
}

func unavailableFileDownloadAccess() *openv1.FileDownloadAccess {
	return &openv1.FileDownloadAccess{
		Availability: openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_UNAVAILABLE,
		Action:       openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE,
	}
}

func uniqueNonEmptyIDs(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
