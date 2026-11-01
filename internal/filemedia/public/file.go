package public

import (
	"errors"
	"slices"
	"strings"
	"time"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"github.com/echovisionlab/geul-api/internal/auth"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"gorm.io/gorm"
)

// Deps are the collaborators of the public File use cases.
type Deps struct {
	DB          *gorm.DB
	SpiceDB     *auth.SpiceDBClient
	Segments    mediaassetpostgres.SegmentConfigLoader
	CDNDomain   string
	MediaDomain string
	MediaSecret string
	// DownloadTTL bounds signed download URLs; zero or more than
	// mediaauth.DownloadTTL signs for mediaauth.DownloadTTL.
	DownloadTTL time.Duration
}

// Service holds the public File use cases: download authorization and the
// public media deliveries other domains' public reads hydrate.
type Service struct {
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	cdnDomain   string
	mediaDomain string
	mediaSecret string
	downloadTTL time.Duration
	segments    mediaassetpostgres.SegmentConfigLoader
}

// NewService validates deps and returns the public File use cases.
func NewService(deps Deps) (*Service, error) {
	var missing []string
	for name, absent := range map[string]bool{"DB": deps.DB == nil, "SpiceDB": deps.SpiceDB == nil, "Segments": deps.Segments == nil} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return nil, errors.New("public file media dependencies are required: " + strings.Join(missing, ", "))
	}
	return &Service{
		db: deps.DB, spiceDB: deps.SpiceDB, segments: deps.Segments,
		cdnDomain: deps.CDNDomain, mediaDomain: deps.MediaDomain, mediaSecret: deps.MediaSecret,
		downloadTTL: boundedMediaTTL(deps.DownloadTTL, mediaauth.DownloadTTL, mediaauth.DownloadTTL),
	}, nil
}

func (s *Service) effectiveDownloadTTL() time.Duration {
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
