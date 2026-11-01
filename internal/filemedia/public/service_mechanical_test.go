package public

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
)

func TestNewServiceNamesEveryMissingDependency(t *testing.T) {
	t.Parallel()

	_, err := NewService(Deps{})
	require.EqualError(t, err, "public file media dependencies are required: DB, Segments, SpiceDB")
}

var errUnusedTestDependency = errors.New("test did not configure this public File dependency")

// newTestService builds the public File use cases through NewService. A test
// that leaves Segments unset gets a loader that fails if reached.
func newTestService(deps Deps) *Service {
	if deps.Segments == nil {
		deps.Segments = unusedSegmentConfigs{}
	}
	service, err := NewService(deps)
	if err != nil {
		panic(err)
	}
	return service
}

type unusedSegmentConfigs struct{}

func (unusedSegmentConfigs) LoadSegmentConfigs(context.Context, *gorm.DB, []*model.AudienceSegment) error {
	return errUnusedTestDependency
}
