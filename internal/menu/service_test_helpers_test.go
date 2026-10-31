package menu

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

// newTestService fills the collaborators deps leaves unset with stand-ins:
// no Site Settings slot or concrete target is referenced, and a
// collaboration checkpoint fails when reached.
func newTestService(t *testing.T, deps Deps) *Service {
	t.Helper()
	if deps.SpiceDB == nil {
		deps.SpiceDB = &auth.SpiceDBClient{}
	}
	if deps.SiteSettings == nil {
		deps.SiteSettings = noopMenuSiteSettingsReferences{}
	}
	if deps.Targets == nil {
		deps.Targets = noopMenuTargetReferences{}
	}
	if deps.Checkpoints == nil {
		deps.Checkpoints = unreachableMenuCheckpoints{}
	}
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	service, err := NewService(deps)
	require.NoError(t, err)
	return service
}

type noopMenuSiteSettingsReferences struct{}

func (noopMenuSiteSettingsReferences) ClearMenuReferences(context.Context, *gorm.DB, string) error {
	return nil
}

type noopMenuTargetReferences struct{}

func (noopMenuTargetReferences) ValidateAndLock(context.Context, *gorm.DB, []TargetReference) error {
	return nil
}

type unreachableMenuCheckpoints struct{}

func (unreachableMenuCheckpoints) RequireCurrentContributorsForPermission(context.Context, *gorm.DB, intrav1.CollaborationResourceType, string, []string, intrav1.CollaborationPermission) error {
	return errors.New("menu collaboration checkpoint is not part of this test")
}

func TestNewServiceNamesEveryMissingDependency(t *testing.T) {
	_, err := NewService(Deps{})
	require.EqualError(t, err, "menu dependencies are required: Audit, Checkpoints, DB, SiteSettings, SpiceDB, Targets")
}
