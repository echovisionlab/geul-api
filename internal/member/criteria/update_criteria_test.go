package criteria_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
	"github.com/echovisionlab/geul-api/internal/member/criteria/criteriatest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestUpdateCriteriaAuditsTheChangedFieldsOnly(t *testing.T) {
	h := newHarness(t)
	criteria := h.store.SeedCriteria(membercriteria.Criteria{Name: "Before", Type: membercriteria.KindFilter})
	updated, err := h.service.UpdateCriteria(h.admin, membercriteria.UpdateCriteriaCommand{ID: criteria.ID, Name: pointer("After")})
	require.NoError(t, err)
	require.Equal(t, "After", updated.Criteria.Name)
	require.True(t, updated.Criteria.UpdatedAt.After(*criteria.UpdatedAt) || updated.Criteria.UpdatedAt.Equal(*criteria.UpdatedAt))
	require.Equal(t, []auditRecord{{
		Action: sharedtelemetry.AuditMemberCriteriaUpdated, TargetType: "member_criteria", TargetID: criteria.ID, ChangedFields: []string{"name"},
	}}, h.auditRecords())
	require.Equal(t, []mutation.Step{
		mutation.StepLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepCommit,
	}, h.units.Steps())

	unchanged, err := h.service.UpdateCriteria(h.admin, membercriteria.UpdateCriteriaCommand{
		ID: criteria.ID, Name: pointer("After"),
		Config:   &membercriteria.CriteriaConfigInput{},
		Observed: &membercriteria.ObservedCriteria{Kind: membercriteria.KindFilter, Config: &membercriteria.CriteriaConfigInput{}},
	})
	require.NoError(t, err)
	require.Equal(t, updated.Criteria.UpdatedAt, unchanged.Criteria.UpdatedAt, "a no-op writes nothing")
	require.Len(t, h.auditRecords(), 1, "a no-op appends no record")
}

func TestUpdateCriteriaMissingCriteriaAnswersNotFoundBeforeAuthorization(t *testing.T) {
	h := newHarness(t, &authztest.Checker{})
	_, err := h.service.UpdateCriteria(h.admin, membercriteria.UpdateCriteriaCommand{ID: uuid.NewString(), Name: pointer("x")})
	require.Equal(t, connect.CodeNotFound, errs.Wrap(err).Code())
	require.Equal(t, "criteria not found", errs.Wrap(err).Message())
	require.Empty(t, h.checker.Checks())
}

func TestUpdateCriteriaDeniedLeavesTheCriteria(t *testing.T) {
	h := newHarness(t, &authztest.Checker{})
	criteria := h.store.SeedCriteria(membercriteria.Criteria{Name: "Kept", Type: membercriteria.KindAllMembers})
	_, err := h.service.UpdateCriteria(h.admin, membercriteria.UpdateCriteriaCommand{ID: criteria.ID, Name: pointer("Changed")})
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())
	stored, _ := h.store.Stored(criteria.ID)
	require.Equal(t, "Kept", stored.Name)
	editCan, err := policyv1.MemberCriteria.Edit(criteria.ID)
	require.Equal(t, []authztest.Check{authztest.Of(h.identityID, editCan, err)}, h.checker.Checks())
	require.Empty(t, h.auditRecords())
}

func TestUpdateKindSwitchWithoutConfigClearsTheObservedFilter(t *testing.T) {
	h := newHarness(t)
	tagID := h.store.SeedMemberTag()
	criteria := h.store.SeedCriteria(membercriteria.Criteria{Name: "Tagged", Type: membercriteria.KindMemberTags, Config: membercriteria.Config{MemberTagIDs: []string{tagID}}})
	desired := membercriteria.KindAllMembers
	updated, err := h.service.UpdateCriteria(h.admin, membercriteria.UpdateCriteriaCommand{
		ID: criteria.ID, Kind: &desired,
		Observed: &membercriteria.ObservedCriteria{Kind: membercriteria.KindMemberTags, Config: &membercriteria.CriteriaConfigInput{MemberTagIDs: []string{tagID}}},
	})
	require.NoError(t, err)
	require.Equal(t, desired, updated.Criteria.Type)
	require.Empty(t, updated.Criteria.Config.MemberTagIDs)
	stored, _ := h.store.Stored(criteria.ID)
	require.Empty(t, stored.Config.MemberTagIDs)
	require.Equal(t, []string{"kind", "member_tag_ids"}, h.auditRecords()[0].ChangedFields)
}

func TestUpdateCriteriaMergesTheFilterAgainstWhatTheEditorObserved(t *testing.T) {
	h := newHarness(t)
	tagA, tagB, peerTag := h.store.SeedMemberTag(), h.store.SeedMemberTag(), h.store.SeedMemberTag()
	criteria := h.store.SeedCriteria(membercriteria.Criteria{Name: "Merged", Type: membercriteria.KindMemberTags, Config: membercriteria.Config{
		MemberTagIDs: []string{tagA, peerTag},
	}})
	updated, err := h.service.UpdateCriteria(h.admin, membercriteria.UpdateCriteriaCommand{
		ID:     criteria.ID,
		Config: &membercriteria.CriteriaConfigInput{MemberTagIDs: []string{tagB}},
		Observed: &membercriteria.ObservedCriteria{
			Kind: membercriteria.KindMemberTags, Config: &membercriteria.CriteriaConfigInput{MemberTagIDs: []string{tagA}},
		},
	})
	require.NoError(t, err)
	want := []string{tagB, peerTag}
	if want[0] > want[1] {
		want[0], want[1] = want[1], want[0]
	}
	require.Equal(t, want, updated.Criteria.Config.MemberTagIDs, "the peer's tag survives the editor's swap")
}

func TestUpdateCriteriaRequiresTheObservedStateForAFilterChange(t *testing.T) {
	h := newHarness(t)
	criteria := h.store.SeedCriteria(membercriteria.Criteria{Name: "x", Type: membercriteria.KindFilter})
	_, err := h.service.UpdateCriteria(h.admin, membercriteria.UpdateCriteriaCommand{ID: criteria.ID, Config: &membercriteria.CriteriaConfigInput{}})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	require.Empty(t, h.units.Steps())
}

func TestUpdateCriteriaIsFrozenByActiveDelivery(t *testing.T) {
	for _, test := range []struct {
		name    string
		seed    func(store *criteriatest.Store, criteriaID string)
		message string
	}{
		{"scheduled campaign", func(store *criteriatest.Store, criteriaID string) {
			store.SeedCampaign(criteriaID, criteriatest.CampaignScheduled)
		}, "Member criteria is used by a scheduled or sending campaign"},
		{"sending delivery run", func(store *criteriatest.Store, criteriaID string) {
			store.SeedCampaign(criteriaID, criteriatest.CampaignDraft)
			store.SeedDeliveryRun(criteriaID, criteriatest.RunSending)
		}, "Member criteria is frozen by an active delivery run"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			criteria := h.store.SeedCriteria(membercriteria.Criteria{Name: "Frozen", Type: membercriteria.KindAllMembers})
			test.seed(h.store, criteria.ID)
			_, err := h.service.UpdateCriteria(h.admin, membercriteria.UpdateCriteriaCommand{ID: criteria.ID, Name: pointer("Changed")})
			require.Equal(t, connect.CodeFailedPrecondition, errs.Wrap(err).Code())
			require.Equal(t, test.message, errs.Wrap(err).Message())
			stored, _ := h.store.Stored(criteria.ID)
			require.Equal(t, "Frozen", stored.Name)
		})
	}
}

func TestUpdateCriteriaKeepsADownloadGrantingCriteriaAnAccessType(t *testing.T) {
	h := newHarness(t)
	criteria := h.store.SeedCriteria(membercriteria.Criteria{Name: "Granting", Type: membercriteria.KindFilter})
	h.store.SeedDownloadPolicy(membercriteria.DownloadPolicy{Owner: "post", ItemID: uuid.NewString(), CriteriaIDs: []string{criteria.ID}})
	desired := membercriteria.KindAllMembers
	_, err := h.service.UpdateCriteria(h.admin, membercriteria.UpdateCriteriaCommand{
		ID: criteria.ID, Kind: &desired,
		Observed: &membercriteria.ObservedCriteria{Kind: membercriteria.KindFilter, Config: &membercriteria.CriteriaConfigInput{}},
	})
	require.Equal(t, connect.CodeFailedPrecondition, errs.Wrap(err).Code())
	require.Equal(t, "Member criteria grants download access and cannot change to this type", errs.Wrap(err).Message())

	desired = membercriteria.KindMemberTags
	tagID := h.store.SeedMemberTag()
	_, err = h.service.UpdateCriteria(h.admin, membercriteria.UpdateCriteriaCommand{
		ID: criteria.ID, Kind: &desired, Config: &membercriteria.CriteriaConfigInput{MemberTagIDs: []string{tagID}},
		Observed: &membercriteria.ObservedCriteria{Kind: membercriteria.KindFilter, Config: &membercriteria.CriteriaConfigInput{}},
	})
	require.NoError(t, err, "another access type keeps the grant")
}

func TestUpdateCriteriaOfAnArchivedCriteriaChangesMetadataAndFilter(t *testing.T) {
	h := newHarness(t)
	archivedAt := time.Now().UTC().Add(-time.Hour)
	criteria := h.store.SeedCriteria(membercriteria.Criteria{Name: "Archived", Type: membercriteria.KindFilter, ArchivedAt: &archivedAt})
	after := time.Date(2026, time.January, 2, 3, 4, 5, 123456789, time.UTC)
	updated, err := h.service.UpdateCriteria(h.admin, membercriteria.UpdateCriteriaCommand{
		ID: criteria.ID, Name: pointer("Renamed"), Description: pointer("Described"),
		Config:   &membercriteria.CriteriaConfigInput{AccountRoles: []string{policyv1.Role.Admin().ID()}, CreatedAfter: &after},
		Observed: &membercriteria.ObservedCriteria{Kind: membercriteria.KindFilter, Config: &membercriteria.CriteriaConfigInput{}},
	})
	require.NoError(t, err)
	require.NotNil(t, updated.Criteria.ArchivedAt)
	require.Equal(t, "Renamed", updated.Criteria.Name)
	require.Equal(t, "Described", *updated.Criteria.Description)
	require.Equal(t, []string{policyv1.Role.Admin().ID()}, updated.Criteria.Config.AccountRoles)
	require.True(t, after.Truncate(time.Microsecond).Equal(*updated.Criteria.Config.CreatedAfter), "the response reads the stored window back")
	require.Equal(t, []string{"account_roles", "created_after", "description", "name"}, h.auditRecords()[0].ChangedFields)
}

func TestUpdateCriteriaRejectsUnknownReferences(t *testing.T) {
	h := newHarness(t)
	tagged := h.store.SeedCriteria(membercriteria.Criteria{Name: "y", Type: membercriteria.KindMemberTags, Config: membercriteria.Config{MemberTagIDs: []string{h.store.SeedMemberTag()}}})
	_, err := h.service.UpdateCriteria(h.admin, membercriteria.UpdateCriteriaCommand{
		ID: tagged.ID, Config: &membercriteria.CriteriaConfigInput{MemberTagIDs: []string{uuid.NewString()}},
		Observed: &membercriteria.ObservedCriteria{Kind: membercriteria.KindMemberTags, Config: &membercriteria.CriteriaConfigInput{MemberTagIDs: tagged.Config.MemberTagIDs}},
	})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	require.Equal(t, "one or more criteria references do not exist", errs.Wrap(err).Message())
	stored, _ := h.store.Stored(tagged.ID)
	require.Equal(t, tagged.Config.MemberTagIDs, stored.Config.MemberTagIDs)
	require.Equal(t, mutation.StepRollback, h.lastStep())
}
