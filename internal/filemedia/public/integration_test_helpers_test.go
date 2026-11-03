//go:build integration

package public_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/stretchr/testify/assert"

	filepublicpostgres "github.com/echovisionlab/geul-api/internal/filemedia/public/postgres"
	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"
	"github.com/echovisionlab/geul-api/internal/model"

	. "github.com/echovisionlab/geul-api/internal/filemedia/public"

	"github.com/echovisionlab/geul-api/internal/auth"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := orytest.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start public File/Media integration suite: %v\n", err)
		os.Exit(1)
	}
	orytest.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	orytest.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close public File/Media integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func newPublicIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := orytest.PrepareOryIntegrationTest(t)
	require.NotNil(t, stack)
	return stack.DB
}

func publicIntegrationSpiceDBClient(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	return orytest.SetupOryStack(t).SpiceDBClient
}

var errUnusedTestDependency = errors.New("test did not configure this public File dependency")

// newTestService builds the public File use cases through NewService. A test
// that leaves CriteriaSet unset gets a loader that fails if reached.
func newTestService(deps testDeps) *Service {
	if deps.CriteriaSet == nil {
		deps.CriteriaSet = unusedCriteriaConfigs{}
	}
	service, err := NewService(Deps{Units: filepublicpostgres.NewUnits(deps.DB, filepublicpostgres.Ports{CriteriaSet: deps.CriteriaSet}), SpiceDB: deps.SpiceDB, CDNDomain: deps.CDNDomain, MediaDomain: deps.MediaDomain, MediaSecret: deps.MediaSecret, DownloadTTL: deps.DownloadTTL})
	if err != nil {
		panic(err)
	}
	return service
}

type unusedCriteriaConfigs struct{}

func (unusedCriteriaConfigs) LoadCriteriaConfigs(context.Context, *gorm.DB, []*model.MemberCriteria) error {
	return errUnusedTestDependency
}

type testDeps struct {
	DB                                  *gorm.DB
	SpiceDB                             *auth.SpiceDBClient
	CriteriaSet                         mediaassetpostgres.CriteriaConfigLoader
	CDNDomain, MediaDomain, MediaSecret string
	DownloadTTL                         time.Duration
}

const publicDownloadUnitSecret = "public-download-unit-secret"

func assertDownloadToken(t *testing.T, signedURL string, fileID string, fileName string, expectedTTL time.Duration) {
	t.Helper()
	parsed, err := url.Parse(signedURL)
	require.NoError(t, err)
	tokenAndPath := strings.TrimPrefix(parsed.Path, "/media/")
	token, _, found := strings.Cut(tokenAndPath, "/")
	require.True(t, found)

	claims, err := mediaauth.ValidateToken(token, publicDownloadUnitSecret)
	require.NoError(t, err)
	assert.Equal(t, mediaauth.PurposeDownload, claims.Purpose)
	assert.Equal(t, mediaauth.ScopeExact, claims.ScopeType)
	assert.Equal(t, "media/"+fileID+".wav", claims.ScopeValue)
	assert.Equal(t, fileName, claims.Filename)
	assert.Equal(t, int64(expectedTTL/time.Second), claims.ExpiryUnix-claims.IssuedAtUnix)
}
