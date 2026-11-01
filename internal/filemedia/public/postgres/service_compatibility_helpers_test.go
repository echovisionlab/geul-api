package filemediapublicpostgres_test

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	. "github.com/echovisionlab/geul-api/internal/filemedia/public"
	filepublicpostgres "github.com/echovisionlab/geul-api/internal/filemedia/public/postgres"
	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
)

var errUnusedTestDependency = errors.New("test did not configure this public File dependency")

// newTestService builds the public File use cases through NewService. A test
// that leaves Segments unset gets a loader that fails if reached.
func newTestService(deps testDeps) *Service {
	if deps.Segments == nil {
		deps.Segments = unusedSegmentConfigs{}
	}
	service, err := NewService(Deps{Units: filepublicpostgres.NewUnits(deps.DB, filepublicpostgres.Ports{Segments: deps.Segments}), SpiceDB: deps.SpiceDB, CDNDomain: deps.CDNDomain, MediaDomain: deps.MediaDomain, MediaSecret: deps.MediaSecret, DownloadTTL: deps.DownloadTTL})
	if err != nil {
		panic(err)
	}
	return service
}

type unusedSegmentConfigs struct{}

func (unusedSegmentConfigs) LoadSegmentConfigs(context.Context, *gorm.DB, []*model.AudienceSegment) error {
	return errUnusedTestDependency
}

type testDeps struct {
	DB                                  *gorm.DB
	SpiceDB                             *auth.SpiceDBClient
	Segments                            mediaassetpostgres.SegmentConfigLoader
	CDNDomain, MediaDomain, MediaSecret string
	DownloadTTL                         time.Duration
}
