package criteria_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestCreateCriteriaStoresTheNormalizedFilterAndGrantsItsPolicy(t *testing.T) {
	h := newHarness(t)
	member := h.store.SeedMember(true)
	after := time.Date(2026, time.January, 2, 3, 4, 5, 123456789, time.FixedZone("KST", 9*60*60))
	created, err := h.service.CreateCriteria(h.admin, membercriteria.CreateCriteriaCommand{
		Name: "Filtered", Kind: membercriteria.KindFilter,
		Config: &membercriteria.CriteriaConfigInput{
			AccountRoles:     []string{policyv1.Role.User().ID(), policyv1.Role.Admin().ID(), policyv1.Role.User().ID()},
			CreatedAfter:     &after,
			ExcludeMemberIDs: []string{" " + member + " ", member},
		},
	})
	require.NoError(t, err)

	require.Equal(t, []string{policyv1.Role.Admin().ID(), policyv1.Role.User().ID()}, created.Criteria.Config.AccountRoles)
	require.Equal(t, []string{member}, created.Criteria.Config.ExcludeMemberIDs)
	require.Equal(t, after.UTC(), *created.Criteria.Config.CreatedAfter, "the response carries the filter as requested")
	require.Zero(t, created.References)
	stored, ok := h.store.Stored(created.Criteria.ID)
	require.True(t, ok)
	require.Equal(t, created.Criteria.Config.AccountRoles, stored.Config.AccountRoles)
	require.True(t, after.Truncate(time.Microsecond).Equal(*stored.Config.CreatedAfter))

	touch, err := policyv1.MemberCriteria.TouchPolicy(created.Criteria.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch}}, h.checker.Applied())
	require.Empty(t, h.checker.Compensated())
	require.Equal(t, []auditRecord{{
		Action: sharedtelemetry.AuditMemberCriteriaCreated, TargetType: "member_criteria", TargetID: created.Criteria.ID,
	}}, h.auditRecords())
	require.Equal(t, []mutation.Step{
		mutation.StepAuthorizationLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit,
		mutation.StepRelationships, mutation.StepCommit,
	}, h.units.Steps())
	createCan, err := policyv1.MemberCriteria.Create()
	require.Equal(t, []authztest.Check{authztest.Of(h.identityID, createCan, err)}, h.checker.Checks())
}

func TestCreateCriteriaRejectsInvalidInputBeforeTheTransaction(t *testing.T) {
	after := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	before := after.Add(-time.Hour)
	for _, test := range []struct {
		name    string
		command membercriteria.CreateCriteriaCommand
		message string
	}{
		{"name", membercriteria.CreateCriteriaCommand{Kind: membercriteria.KindAllMembers}, "name is required"},
		{"type", membercriteria.CreateCriteriaCommand{Name: "x", Kind: "MEMBER_CRITERIA_KIND_UNSPECIFIED"}, "criteria type is required"},
		{"unknown type", membercriteria.CreateCriteriaCommand{Name: "x", Kind: "MEMBER_CRITERIA_KIND_RETIRED"}, "unknown criteria type: MEMBER_CRITERIA_KIND_RETIRED"},
		{"role", membercriteria.CreateCriteriaCommand{
			Name: "x", Kind: membercriteria.KindFilter, Config: &membercriteria.CriteriaConfigInput{AccountRoles: []string{""}},
		}, "account_roles contains an unsupported role"},
		{"tag", membercriteria.CreateCriteriaCommand{
			Name: "x", Kind: membercriteria.KindMemberTags, Config: &membercriteria.CriteriaConfigInput{MemberTagIDs: []string{"not-a-uuid"}},
		}, "member_tag_ids contains an invalid UUID"},
		{"window before the type", membercriteria.CreateCriteriaCommand{
			Name: "x", Kind: membercriteria.KindAllMembers, Config: &membercriteria.CriteriaConfigInput{CreatedAfter: &after, CreatedBefore: &before},
		}, "created_after must not be later than created_before"},
		{"filter of the type", membercriteria.CreateCriteriaCommand{
			Name: "x", Kind: membercriteria.KindMemberTags,
		}, "member-tag Member criteria require member_tag_ids"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			_, err := h.service.CreateCriteria(h.admin, test.command)
			require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
			require.Equal(t, test.message, errs.Wrap(err).Message())
			require.Empty(t, h.units.Steps())
		})
	}
}

func TestCreateCriteriaRequiresAnAuthenticatedAdministrator(t *testing.T) {
	h := newHarness(t, &authztest.Checker{})
	_, err := h.service.CreateCriteria(context.Background(), membercriteria.CreateCriteriaCommand{})
	require.Equal(t, connect.CodeUnauthenticated, errs.Wrap(err).Code(), "authentication is checked before validation")

	_, err = h.service.CreateCriteria(h.admin, membercriteria.CreateCriteriaCommand{Name: "Denied", Kind: membercriteria.KindAllMembers})
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())
	require.Equal(t, "admin access required", errs.Wrap(err).Message())
	require.Zero(t, h.store.CriteriaCount())
	require.Empty(t, h.checker.Applied())
	require.Equal(t, mutation.StepRollback, h.lastStep())
}

func TestCreateCriteriaRollsBackWhenAuditOrTheRelationshipFails(t *testing.T) {
	auditDown := errors.New("audit unavailable")
	h := newHarness(t)
	h.audit.Err = auditDown
	_, err := h.service.CreateCriteria(h.admin, membercriteria.CreateCriteriaCommand{Name: "Unaudited", Kind: membercriteria.KindAllMembers})
	require.ErrorIs(t, err, auditDown)
	require.Zero(t, h.store.CriteriaCount())
	require.Empty(t, h.checker.Applied(), "the relationship is written after the audit")
	require.Equal(t, mutation.StepRollback, h.lastStep())

	spiceDBDown := errors.New("SpiceDB unavailable")
	h = newHarness(t, authztest.AllowAll().FailWrites(spiceDBDown))
	_, err = h.service.CreateCriteria(h.admin, membercriteria.CreateCriteriaCommand{Name: "Ungranted", Kind: membercriteria.KindAllMembers})
	require.ErrorIs(t, err, spiceDBDown)
	require.Zero(t, h.store.CriteriaCount())
	require.Equal(t, mutation.StepRollback, h.lastStep())
}

func TestCreateCriteriaRejectsUnknownReferences(t *testing.T) {
	h := newHarness(t)
	_, err := h.service.CreateCriteria(h.admin, membercriteria.CreateCriteriaCommand{
		Name: "Unknown tag", Kind: membercriteria.KindMemberTags,
		Config: &membercriteria.CriteriaConfigInput{MemberTagIDs: []string{uuid.NewString()}},
	})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	require.Equal(t, "one or more criteria references do not exist", errs.Wrap(err).Message())
	require.Zero(t, h.store.CriteriaCount())
	require.Empty(t, h.checker.Applied())
}

func TestEstimateCriteriaCountCountsTheUnsavedFilter(t *testing.T) {
	h := newHarness(t)
	h.counter.count = 7
	tagID := uuid.NewString()
	count, err := h.service.EstimateCriteriaCount(h.admin, membercriteria.EstimateCriteriaCountQuery{
		Kind: membercriteria.KindMemberTags, Config: &membercriteria.CriteriaConfigInput{MemberTagIDs: []string{tagID}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 7, count)
	require.Equal(t, []membercriteria.Criteria{{Type: membercriteria.KindMemberTags, Config: membercriteria.Config{
		MemberTagIDs: []string{tagID}, AccountRoles: []string{}, ExcludeMemberIDs: []string{},
	}}}, h.counter.criteriaSet)
	require.Empty(t, h.units.Steps(), "an estimate opens no transaction")

	_, err = h.service.EstimateCriteriaCount(h.admin, membercriteria.EstimateCriteriaCountQuery{Kind: membercriteria.KindMemberTags})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())

	counterDown := errors.New("count failed")
	h.counter.err = counterDown
	_, err = h.service.EstimateCriteriaCount(h.admin, membercriteria.EstimateCriteriaCountQuery{Kind: membercriteria.KindAllMembers})
	require.ErrorIs(t, err, counterDown)

	denied := newHarness(t, &authztest.Checker{})
	_, err = denied.service.EstimateCriteriaCount(denied.admin, membercriteria.EstimateCriteriaCountQuery{Kind: membercriteria.KindAllMembers})
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())
	require.Equal(t, "admin access required", errs.Wrap(err).Message())
	require.Empty(t, denied.counter.criteriaSet)
}

func TestCreateCriteriaRejectsAnIneligibleExcludedMember(t *testing.T) {
	h := newHarness(t)
	for name, memberID := range map[string]string{
		"not onboarded": h.store.SeedMember(false),
		"missing":       uuid.NewString(),
	} {
		_, err := h.service.CreateCriteria(h.admin, membercriteria.CreateCriteriaCommand{
			Name: "Excluding", Kind: membercriteria.KindFilter,
			Config: &membercriteria.CriteriaConfigInput{ExcludeMemberIDs: []string{memberID}},
		})
		require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code(), name)
		require.Equal(t, "every excluded member must be an onboarded Member with an exact Identity link", errs.Wrap(err).Message(), name)
	}
	require.Zero(t, h.store.CriteriaCount())
	require.Empty(t, h.checker.Applied())
}
