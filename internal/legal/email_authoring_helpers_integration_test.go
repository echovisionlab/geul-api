//go:build integration

package legal_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// newEmailAuthoringService builds the Email Authoring management use cases
// that seed fixtures. A test send fails, and an unset audit appender
// records in memory.
func newEmailAuthoringService(t *testing.T, deps emailauthoring.Deps) *emailauthoring.Service {
	t.Helper()
	runtime := emailauthoringadapter.NewRuntime()
	if deps.Publisher == nil {
		deps.Publisher = unusedEmailPublisher{}
	}
	if deps.Runtime == nil {
		deps.Runtime = runtime
	}
	if deps.RenderData == nil {
		deps.RenderData = runtime
	}
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	service, err := emailauthoring.NewService(deps)
	require.NoError(t, err)
	return service
}

type unusedEmailPublisher struct{}

func (unusedEmailPublisher) PublishSendEmail(context.Context, *managev1.SendEmailEvent) error {
	return errors.New("test did not configure an email publisher")
}

// newInternalEmailAuthoringService builds the Email Authoring collaboration
// use cases that seed fixtures.
func newInternalEmailAuthoringService(t *testing.T, deps emailauthoring.InternalDeps) *emailauthoring.InternalService {
	t.Helper()
	deps.Members = memberadapter.ContributorMembers{}
	service, err := emailauthoring.NewInternalService(deps)
	require.NoError(t, err)
	return service
}
