package public

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/mutation"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
)

// Deps are the collaborators of the public File use cases.
type Deps struct {
	Units       mutation.UnitOfWork[Store]
	SpiceDB     authz.AuthorizationDecisionChecker
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
	units       mutation.UnitOfWork[Store]
	spiceDB     authz.AuthorizationDecisionChecker
	cdnDomain   string
	mediaDomain string
	mediaSecret string
	downloadTTL time.Duration
}

// NewService validates deps and returns the public File use cases.
func NewService(deps Deps) (*Service, error) {
	var missing []string
	for name, absent := range map[string]bool{"Units": deps.Units == nil, "SpiceDB": deps.SpiceDB == nil} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return nil, errors.New("public file media dependencies are required: " + strings.Join(missing, ", "))
	}
	return &Service{
		units: deps.Units, spiceDB: deps.SpiceDB,
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
