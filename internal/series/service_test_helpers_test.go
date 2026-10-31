package series

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// newTestService fills the collaborators deps leaves unset with stand-ins
// that fail when reached, and writes Post membership through the Post
// functions.
func newTestService(t *testing.T, deps Deps) *Service {
	t.Helper()
	if deps.SpiceDB == nil {
		deps.SpiceDB = &auth.SpiceDBClient{}
	}
	if deps.Identity == nil {
		deps.Identity = unreachableSeriesIdentity{}
	}
	unreachable := unreachableSeriesCollaborator{}
	if deps.Media == nil {
		deps.Media = unreachable
	}
	if deps.OG == nil {
		deps.OG = unreachable
	}
	if deps.Reads == nil {
		deps.Reads = unreachable
	}
	if deps.MenuTargets == nil {
		deps.MenuTargets = unreachable
	}
	if deps.Posts == nil {
		deps.Posts = unreachable
	}
	if deps.Membership == nil {
		deps.Membership = testPostMembership{}
	}
	if deps.Members == nil {
		deps.Members = unreachable
	}
	if deps.Checkpoints == nil {
		deps.Checkpoints = unreachable
	}
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	service, err := NewService(deps)
	require.NoError(t, err)
	return service
}

// seriesUnitContext is a member request, so mutations can name their audit
// records.
func seriesUnitContext(t *testing.T) context.Context {
	t.Helper()
	request, err := sharedtelemetry.NewPublicRequestContext("192.0.2.30")
	require.NoError(t, err)
	ctx := sharedtelemetry.WithRequestContext(t.Context(), request)
	return sharedtelemetry.WithActor(ctx, sharedtelemetry.MemberActor{MemberID: uuid.NewString()})
}

var errUnreachableSeriesCollaborator = errors.New("series collaborator is not part of this test")

type unreachableSeriesIdentity struct{ auth.IdentityManager }

type unreachableSeriesCollaborator struct{}

func (unreachableSeriesCollaborator) ReadyAsset(context.Context, ...*string) (*commonv1.AssetRef, error) {
	return nil, errUnreachableSeriesCollaborator
}
func (unreachableSeriesCollaborator) ReadyAssets(context.Context, ...*string) (map[string]*commonv1.AssetRef, error) {
	return nil, errUnreachableSeriesCollaborator
}
func (unreachableSeriesCollaborator) ReadyAssetsForSourceFiles(context.Context, string, []string) (map[string]*commonv1.AssetRef, error) {
	return nil, errUnreachableSeriesCollaborator
}
func (unreachableSeriesCollaborator) ReadyAssetForSourceFile(context.Context, string, string) (*commonv1.AssetRef, error) {
	return nil, errUnreachableSeriesCollaborator
}
func (unreachableSeriesCollaborator) RequireAttachableFile(context.Context, *gorm.DB, string) error {
	return errUnreachableSeriesCollaborator
}
func (unreachableSeriesCollaborator) BindFeaturedImage(context.Context, *gorm.DB, string, string) (*commonv1.AssetRef, error) {
	return nil, errUnreachableSeriesCollaborator
}
func (unreachableSeriesCollaborator) ReleaseFeaturedImage(context.Context, *gorm.DB, string) error {
	return errUnreachableSeriesCollaborator
}
func (unreachableSeriesCollaborator) CancelAndReleaseOG(context.Context, *gorm.DB, string) error {
	return errUnreachableSeriesCollaborator
}
func (unreachableSeriesCollaborator) RequestCurrent(context.Context, *gorm.DB, string, string, bool, string) (*string, error) {
	return nil, errUnreachableSeriesCollaborator
}
func (unreachableSeriesCollaborator) LoadPostCounts(context.Context, []string) (map[string]int32, error) {
	return nil, errUnreachableSeriesCollaborator
}
func (unreachableSeriesCollaborator) LoadManagerCounts(context.Context, []string) (map[string]int32, error) {
	return nil, errUnreachableSeriesCollaborator
}
func (unreachableSeriesCollaborator) UpdateSlug(context.Context, *gorm.DB, string, string, string, string) error {
	return errUnreachableSeriesCollaborator
}
func (unreachableSeriesCollaborator) Remove(context.Context, *gorm.DB, string, string, string) error {
	return errUnreachableSeriesCollaborator
}
func (unreachableSeriesCollaborator) RequireLockedEdit(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error {
	return errUnreachableSeriesCollaborator
}
func (unreachableSeriesCollaborator) LoadSeriesManagers(context.Context, []string) (map[string]*managev1.SeriesManager, error) {
	return nil, errUnreachableSeriesCollaborator
}
func (unreachableSeriesCollaborator) RequireCurrentContributors(context.Context, *gorm.DB, intrav1.CollaborationResourceType, string, []string) error {
	return errUnreachableSeriesCollaborator
}

func TestNewServiceNamesEveryMissingDependency(t *testing.T) {
	_, err := NewService(Deps{})
	require.EqualError(t, err, "series dependencies are required: Audit, Checkpoints, DB, Identity, Media, Members, Membership, MenuTargets, OG, Posts, Reads, SpiceDB")
}
