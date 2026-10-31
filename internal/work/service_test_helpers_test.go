package work

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// newTestService fills the collaborators deps leaves unset with stand-ins
// that fail when reached.
func newTestService(t *testing.T, deps Deps) *Service {
	t.Helper()
	unreachable := unreachableWorkCollaborator{}
	if deps.SpiceDB == nil {
		deps.SpiceDB = &auth.SpiceDBClient{}
	}
	if deps.Runtime == nil {
		deps.Runtime = unreachable
	}
	if deps.Signals == nil {
		deps.Signals = unreachable
	}
	if deps.ContentBlocks == nil {
		deps.ContentBlocks = &contentblock.Store{}
	}
	if deps.Media == nil {
		deps.Media = unreachable
	}
	if deps.Members == nil {
		deps.Members = unreachable
	}
	if deps.Checkpoints == nil {
		deps.Checkpoints = unreachable
	}
	if deps.ShareLinks == nil {
		deps.ShareLinks = unreachable
	}
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	service, err := NewService(deps)
	require.NoError(t, err)
	return service
}

var errUnreachableWorkCollaborator = errors.New("work collaborator is not part of this test")

type unreachableWorkCollaborator struct{}

func (unreachableWorkCollaborator) RequestCurrentWithDB(context.Context, *gorm.DB, managev1.OgEntityType, string, string, bool, string) (string, error) {
	return "", errUnreachableWorkCollaborator
}
func (unreachableWorkCollaborator) CancelAndReleaseEntityWithDB(context.Context, *gorm.DB, managev1.OgEntityType, string, string) error {
	return errUnreachableWorkCollaborator
}
func (unreachableWorkCollaborator) LockAttachableFilesForUpdate(context.Context, *gorm.DB, []string) error {
	return errUnreachableWorkCollaborator
}
func (unreachableWorkCollaborator) ReleasePublicAssetBindings(context.Context, *gorm.DB, string, string, string) error {
	return errUnreachableWorkCollaborator
}
func (unreachableWorkCollaborator) ResolveReadyAssetRefs(context.Context, *gorm.DB, []string) (map[string]*commonv1.AssetRef, error) {
	return nil, errUnreachableWorkCollaborator
}
func (unreachableWorkCollaborator) ReadyPublicAssetRefForSourceFile(context.Context, *gorm.DB, string, string) (*commonv1.AssetRef, error) {
	return nil, errUnreachableWorkCollaborator
}
func (unreachableWorkCollaborator) BindReadyAssetForSourceFile(context.Context, *gorm.DB, string, string, string, string, string) (*commonv1.AssetRef, error) {
	return nil, errUnreachableWorkCollaborator
}
func (unreachableWorkCollaborator) LoadUnavailableVersionAttachmentKinds(context.Context, *gorm.DB, proto.Message) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error) {
	return nil, errUnreachableWorkCollaborator
}
func (unreachableWorkCollaborator) NotifyProtobufWithExecutor(context.Context, eventpkg.DBTX, string, proto.Message) error {
	return errUnreachableWorkCollaborator
}
func (unreachableWorkCollaborator) HydrateAuthorizedWorkBlockMediaWithDB(context.Context, *gorm.DB, string, uuid.UUID, *auth.UserInfo, []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
	return nil, errUnreachableWorkCollaborator
}
func (unreachableWorkCollaborator) LoadMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error) {
	return nil, errUnreachableWorkCollaborator
}
func (unreachableWorkCollaborator) RequireCurrentContributors(context.Context, *gorm.DB, intrav1.CollaborationResourceType, string, []string) error {
	return errUnreachableWorkCollaborator
}
func (unreachableWorkCollaborator) DeleteEntityShareLinks(context.Context, *gorm.DB, string) error {
	return errUnreachableWorkCollaborator
}

func TestNewServiceNamesEveryMissingDependency(t *testing.T) {
	_, err := NewService(Deps{})
	require.EqualError(t, err, "work dependencies are required: Audit, Checkpoints, ContentBlocks, DB, Media, Members, Runtime, ShareLinks, Signals, SpiceDB")
}
