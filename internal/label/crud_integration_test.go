//go:build integration

package label

import (
	"testing"

	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func newLabelIntegrationService(
	t *testing.T,
	db *gorm.DB,
	adminID string,
	fileDeleter *recordingArtistFileDeleter,
) *LabelService {
	t.Helper()
	stack := testutil.SetupOryStack(t)
	syncArtistIntegrationGlobalRole(t, stack.SpiceDBClient, adminID, policyv1.Role.Admin())

	return NewLabelService(
		db,
		stack.SpiceDBClient,
		&fakeIdentityManager{identity: postIntegrationIdentity(adminID, "en")},
		fileDeleter,
		noopArtistAsyncPublisher{},
		Dependencies{
			Translation: labeladapter.NewTranslation(),
			Members:     labeladapter.NewMemberProjection(db, ""),
			Runtime:     newLabelRuntimeForTest(db, ""),
		},
		WithLabelContentBlockStore(newPageIntegrationContentBlockStore(t, stack.SpiceDBClient)),
	)
}
