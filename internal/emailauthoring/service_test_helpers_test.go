//go:build integration

package emailauthoring

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

var errUnusedTestDependency = errors.New("test did not configure this Email Authoring dependency")

// newTestService builds the management use cases through NewService. Fields
// left unset in deps get stand-ins that fail if a test reaches them, the
// runtime fixture, render data that only adds the site origin, and a
// recording audit appender.
func newTestService(t *testing.T, deps Deps) *Service {
	t.Helper()
	if deps.SpiceDB == nil {
		deps.SpiceDB = &auth.SpiceDBClient{}
	}
	if deps.ContentBlocks == nil {
		deps.ContentBlocks = &contentblock.Store{}
	}
	if deps.Publisher == nil {
		deps.Publisher = unusedPublisher{}
	}
	if deps.Runtime == nil {
		deps.Runtime = emailTemplateRuntimeFixture{}
	}
	if deps.RenderData == nil {
		deps.RenderData = siteOriginRenderData{}
	}
	if deps.References == nil {
		deps.References = unusedReferences{}
	}
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	service, err := NewService(deps)
	require.NoError(t, err)
	return service
}

// newTestInternalService builds the collaboration and AI document use cases
// through NewInternalService with the same kind of stand-ins; Members locks
// real Member rows.
func newTestInternalService(t *testing.T, deps InternalDeps) *InternalService {
	t.Helper()
	if deps.SpiceDB == nil {
		deps.SpiceDB = &auth.SpiceDBClient{}
	}
	if deps.ContentBlocks == nil {
		deps.ContentBlocks = &contentblock.Store{}
	}
	if deps.Checkpoints == nil {
		deps.Checkpoints = unusedCheckpoints{}
	}
	if deps.References == nil {
		deps.References = unusedReferences{}
	}
	if deps.Members == nil {
		deps.Members = testMembers{}
	}
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	service, err := NewInternalService(deps)
	require.NoError(t, err)
	return service
}

type unusedPublisher struct{}

func (unusedPublisher) PublishSendEmail(context.Context, *managev1.SendEmailEvent) error {
	return errUnusedTestDependency
}

// siteOriginRenderData adds only the site origin to preview render data.
type siteOriginRenderData struct{}

func (siteOriginRenderData) BuildEmailRenderData(_ context.Context, _ *gorm.DB, _, siteOrigin, _ string, input map[string]string) map[string]string {
	input["site_origin"] = siteOrigin
	return input
}

type unusedReferences struct{}

func (unusedReferences) TemplateDeliveryRunCounts(context.Context, *gorm.DB, []string) (map[string]int32, error) {
	return nil, errUnusedTestDependency
}

func (unusedReferences) LayoutExternalReferenceCounts(context.Context, *gorm.DB, []string) (map[string]LayoutExternalReferenceCounts, error) {
	return nil, errUnusedTestDependency
}

func (unusedReferences) RequireTemplateMutable(context.Context, *gorm.DB, string) error {
	return errUnusedTestDependency
}

func (unusedReferences) RequireLayoutMutable(context.Context, *gorm.DB, string) error {
	return errUnusedTestDependency
}

func (unusedReferences) DetachTemplateHistory(context.Context, *gorm.DB, string) error {
	return errUnusedTestDependency
}

func (unusedReferences) DetachLayoutHistory(context.Context, *gorm.DB, string) error {
	return errUnusedTestDependency
}

type unusedCheckpoints struct{}

func (unusedCheckpoints) RequireCurrentContributors(context.Context, *gorm.DB, intrav1.CollaborationResourceType, string, []string) error {
	return errUnusedTestDependency
}

// testMembers locks Member rows like memberadapter.ContributorMembers.
type testMembers struct{}

func (testMembers) LockMembers(ctx context.Context, tx *gorm.DB, memberIDs []string) ([]string, error) {
	locked := []string{}
	err := tx.WithContext(ctx).Table("member").Clauses(clause.Locking{Strength: "KEY SHARE"}).Where("id IN ?", memberIDs).Pluck("id::text", &locked).Error
	return locked, err
}
