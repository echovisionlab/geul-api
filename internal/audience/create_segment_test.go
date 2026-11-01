package audience_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestCreateSegmentStoresTheNormalizedFilterAndGrantsItsPolicy(t *testing.T) {
	h := newHarness(t)
	member := h.store.SeedMember(true)
	after := time.Date(2026, time.January, 2, 3, 4, 5, 123456789, time.FixedZone("KST", 9*60*60))
	created, err := h.service.CreateSegment(h.admin, audience.CreateSegmentCommand{
		Name: "Filtered", SegmentType: audience.SegmentMembersByFilter,
		Config: &audience.SegmentConfigInput{
			AccountRoles:     []string{policyv1.Role.User().ID(), policyv1.Role.Admin().ID(), policyv1.Role.User().ID()},
			CreatedAfter:     &after,
			ExcludeMemberIDs: []string{" " + member + " ", member},
		},
	})
	require.NoError(t, err)

	require.Equal(t, []string{policyv1.Role.Admin().ID(), policyv1.Role.User().ID()}, created.Segment.Config.AccountRoles)
	require.Equal(t, []string{member}, created.Segment.Config.ExcludeMemberIDs)
	require.Equal(t, after.UTC(), *created.Segment.Config.CreatedAfter, "the response carries the filter as requested")
	require.Zero(t, created.References)
	stored, ok := h.store.Stored(created.Segment.ID)
	require.True(t, ok)
	require.Equal(t, created.Segment.Config.AccountRoles, stored.Config.AccountRoles)
	require.True(t, after.Truncate(time.Microsecond).Equal(*stored.Config.CreatedAfter))

	touch, err := policyv1.AudienceSegment.TouchPolicy(created.Segment.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch}}, h.checker.Applied())
	require.Empty(t, h.checker.Compensated())
	require.Equal(t, []auditRecord{{
		Action: sharedtelemetry.AuditAudienceSegmentCreated, TargetType: "audience_segment", TargetID: created.Segment.ID,
	}}, h.auditRecords())
	require.Equal(t, []mutation.Step{
		mutation.StepAuthorizationLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit,
		mutation.StepRelationships, mutation.StepCommit,
	}, h.units.Steps())
	createCan, err := policyv1.AudienceSegment.Create()
	require.Equal(t, []authztest.Check{authztest.Of(h.identityID, createCan, err)}, h.checker.Checks())
}

func TestCreateSegmentRejectsInvalidInputBeforeTheTransaction(t *testing.T) {
	after := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	before := after.Add(-time.Hour)
	for _, test := range []struct {
		name    string
		command audience.CreateSegmentCommand
		message string
	}{
		{"name", audience.CreateSegmentCommand{SegmentType: audience.SegmentAllMembers}, "name is required"},
		{"type", audience.CreateSegmentCommand{Name: "x", SegmentType: "SEGMENT_TYPE_UNSPECIFIED"}, "segment type is required"},
		{"unknown type", audience.CreateSegmentCommand{Name: "x", SegmentType: "SEGMENT_TYPE_RETIRED"}, "unknown segment type: SEGMENT_TYPE_RETIRED"},
		{"role", audience.CreateSegmentCommand{
			Name: "x", SegmentType: audience.SegmentMembersByFilter, Config: &audience.SegmentConfigInput{AccountRoles: []string{""}},
		}, "account_roles contains an unsupported role"},
		{"tag", audience.CreateSegmentCommand{
			Name: "x", SegmentType: audience.SegmentMemberTags, Config: &audience.SegmentConfigInput{MemberTagIDs: []string{"not-a-uuid"}},
		}, "member_tag_ids contains an invalid UUID"},
		{"window before the type", audience.CreateSegmentCommand{
			Name: "x", SegmentType: audience.SegmentAllMembers, Config: &audience.SegmentConfigInput{CreatedAfter: &after, CreatedBefore: &before},
		}, "created_after must not be later than created_before"},
		{"filter of the type", audience.CreateSegmentCommand{
			Name: "x", SegmentType: audience.SegmentMemberTags,
		}, "member-tag segments require member_tag_ids"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			_, err := h.service.CreateSegment(h.admin, test.command)
			require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
			require.Equal(t, test.message, errs.Wrap(err).Message())
			require.Empty(t, h.units.Steps())
		})
	}
}

func TestCreateSegmentRequiresAnAuthenticatedAdministrator(t *testing.T) {
	h := newHarness(t, &authztest.Checker{})
	_, err := h.service.CreateSegment(context.Background(), audience.CreateSegmentCommand{})
	require.Equal(t, connect.CodeUnauthenticated, errs.Wrap(err).Code(), "authentication is checked before validation")

	_, err = h.service.CreateSegment(h.admin, audience.CreateSegmentCommand{Name: "Denied", SegmentType: audience.SegmentAllMembers})
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())
	require.Equal(t, "admin access required", errs.Wrap(err).Message())
	require.Zero(t, h.store.SegmentCount())
	require.Empty(t, h.checker.Applied())
	require.Equal(t, mutation.StepRollback, h.lastStep())
}

func TestCreateSegmentRollsBackWhenAuditOrTheRelationshipFails(t *testing.T) {
	auditDown := errors.New("audit unavailable")
	h := newHarness(t)
	h.audit.Err = auditDown
	_, err := h.service.CreateSegment(h.admin, audience.CreateSegmentCommand{Name: "Unaudited", SegmentType: audience.SegmentAllMembers})
	require.ErrorIs(t, err, auditDown)
	require.Zero(t, h.store.SegmentCount())
	require.Empty(t, h.checker.Applied(), "the relationship is written after the audit")
	require.Equal(t, mutation.StepRollback, h.lastStep())

	spiceDBDown := errors.New("SpiceDB unavailable")
	h = newHarness(t, authztest.AllowAll().FailWrites(spiceDBDown))
	_, err = h.service.CreateSegment(h.admin, audience.CreateSegmentCommand{Name: "Ungranted", SegmentType: audience.SegmentAllMembers})
	require.ErrorIs(t, err, spiceDBDown)
	require.Zero(t, h.store.SegmentCount())
	require.Equal(t, mutation.StepRollback, h.lastStep())
}

func TestCreateSegmentRejectsUnknownReferences(t *testing.T) {
	h := newHarness(t)
	_, err := h.service.CreateSegment(h.admin, audience.CreateSegmentCommand{
		Name: "Unknown tag", SegmentType: audience.SegmentMemberTags,
		Config: &audience.SegmentConfigInput{MemberTagIDs: []string{uuid.NewString()}},
	})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	require.Equal(t, "one or more audience references do not exist", errs.Wrap(err).Message())
	require.Zero(t, h.store.SegmentCount())
	require.Empty(t, h.checker.Applied())
}

func TestEstimateSegmentCountCountsTheUnsavedFilter(t *testing.T) {
	h := newHarness(t)
	h.counter.count = 7
	tagID := uuid.NewString()
	count, err := h.service.EstimateSegmentCount(h.admin, audience.EstimateSegmentCountQuery{
		SegmentType: audience.SegmentMemberTags, Config: &audience.SegmentConfigInput{MemberTagIDs: []string{tagID}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 7, count)
	require.Equal(t, []audience.Segment{{Type: audience.SegmentMemberTags, Config: audience.Config{
		MemberTagIDs: []string{tagID}, AccountRoles: []string{}, ExcludeMemberIDs: []string{},
	}}}, h.counter.segments)
	require.Empty(t, h.units.Steps(), "an estimate opens no transaction")

	_, err = h.service.EstimateSegmentCount(h.admin, audience.EstimateSegmentCountQuery{SegmentType: audience.SegmentMemberTags})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())

	counterDown := errors.New("count failed")
	h.counter.err = counterDown
	_, err = h.service.EstimateSegmentCount(h.admin, audience.EstimateSegmentCountQuery{SegmentType: audience.SegmentAllMembers})
	require.ErrorIs(t, err, counterDown)

	denied := newHarness(t, &authztest.Checker{})
	_, err = denied.service.EstimateSegmentCount(denied.admin, audience.EstimateSegmentCountQuery{SegmentType: audience.SegmentAllMembers})
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())
	require.Equal(t, "admin access required", errs.Wrap(err).Message())
	require.Empty(t, denied.counter.segments)
}
