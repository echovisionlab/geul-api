package public

import (
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"gorm.io/gorm"
)

type FileService struct {
	openv1connect.UnimplementedFileServiceHandler
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	cdnDomain   string
	mediaDomain string
	mediaSecret string
	downloadTTL time.Duration
	segments    mediaasset.SegmentConfigLoader
}

type FileServiceOption func(*FileService)

func WithDownloadSegmentConfigs(loader mediaasset.SegmentConfigLoader) FileServiceOption {
	return func(service *FileService) { service.segments = loader }
}

func (s *FileService) effectiveDownloadTTL() time.Duration {
	if s != nil {
		return boundedMediaTTL(s.downloadTTL, mediaauth.DownloadTTL, mediaauth.DownloadTTL)
	}
	return mediaauth.DownloadTTL
}

func boundedMediaTTL(requested, fallback, maximum time.Duration) time.Duration {
	if requested <= 0 {
		return fallback
	}
	if requested > maximum {
		return maximum
	}
	return requested
}

func NewFileService(
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	cdnDomain string,
	mediaDomain string,
	mediaSecret string,
	downloadTTL time.Duration,
	options ...FileServiceOption,
) *FileService {
	if db == nil || spiceDB == nil {
		panic("public file service dependencies are required")
	}
	service := &FileService{
		db:          db,
		spiceDB:     spiceDB,
		cdnDomain:   cdnDomain,
		mediaDomain: mediaDomain,
		mediaSecret: mediaSecret,
		downloadTTL: boundedMediaTTL(downloadTTL, mediaauth.DownloadTTL, mediaauth.DownloadTTL),
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}
