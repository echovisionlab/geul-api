package member_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/member"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func tagRecords(f *fixture, action sharedtelemetry.AuditAction) []sharedtelemetry.AuditRecord {
	var records []sharedtelemetry.AuditRecord
	for _, record := range f.audit.Records() {
		if record.Action == action {
			records = append(records, record)
		}
	}
	return records
}

func TestMemberTagAuditsCommitExactLifecycleAndSkipRejectedMutations(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created, err := f.service.CreateMemberTag(f.ctx(t), member.CreateMemberTagCommand{Name: "  Featured members  "})
	require.NoError(t, err)
	require.Equal(t, "Featured members", created.Name)
	createdRecords := tagRecords(f, sharedtelemetry.AuditMemberTagCreated)
	require.Len(t, createdRecords, 1)
	require.Equal(t, created.ID, createdRecords[0].TargetID)
	require.Equal(t, "Featured members", createdRecords[0].TagName)
	require.Equal(t, f.memberID, createdRecords[0].MemberID)

	_, err = f.service.CreateMemberTag(f.ctx(t), member.CreateMemberTagCommand{Name: "Featured members"})
	require.Equal(t, connect.CodeAlreadyExists, errs.Wrap(err).Code())
	require.ErrorContains(t, err, "member tag with name 'Featured members' already exists")
	require.Len(t, f.store.Tags(), 1)
	require.Len(t, tagRecords(f, sharedtelemetry.AuditMemberTagCreated), 1)

	first, _ := f.seedMember("first")
	second, _ := f.seedMember("second")
	f.store.SeedMemberTags(first, created.ID)
	f.store.SeedMemberTags(second, created.ID)
	require.NoError(t, f.service.DeleteMemberTag(f.ctx(t), member.DeleteMemberTagCommand{ID: created.ID}))
	deletedRecords := tagRecords(f, sharedtelemetry.AuditMemberTagDeleted)
	require.Len(t, deletedRecords, 1)
	require.Equal(t, "Featured members", deletedRecords[0].TagName)
	require.Empty(t, f.store.MemberTags(first))
	require.Empty(t, f.store.MemberTags(second))
	require.Empty(t, f.memberUpdates())

	_, err = f.service.CreateMemberTag(f.ctx(t), member.CreateMemberTagCommand{Name: "  "})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	err = f.service.DeleteMemberTag(f.ctx(t), member.DeleteMemberTagCommand{ID: created.ID})
	require.Equal(t, connect.CodeNotFound, errs.Wrap(err).Code())
	require.Len(t, tagRecords(f, sharedtelemetry.AuditMemberTagDeleted), 1)
}

func TestMemberTagDeleteRejectsCriteriaReferencesWithoutAudit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tagID := uuid.NewString()
	f.store.SeedTag(member.Tag{ID: tagID, Name: "criteriaed"})
	f.store.SeedCriteriaTag(tagID)

	err := f.service.DeleteMemberTag(f.ctx(t), member.DeleteMemberTagCommand{ID: tagID})

	require.Equal(t, connect.CodeFailedPrecondition, errs.Wrap(err).Code())
	require.ErrorContains(t, err, "member tag is referenced by a Member criteria")
	require.Contains(t, f.store.Tags(), tagID)
	require.Empty(t, tagRecords(f, sharedtelemetry.AuditMemberTagDeleted))
}

func TestMemberTagAuditAppendFailureRollsBackCreateAndDelete(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	failing := f.newService(t, &audittest.Recorder{Err: errAuditUnavailable})
	_, err := failing.CreateMemberTag(f.ctx(t), member.CreateMemberTagCommand{Name: "rolled back"})
	require.Equal(t, connect.CodeInternal, errs.Wrap(err).Code())
	require.Empty(t, f.store.Tags())

	tagID := uuid.NewString()
	f.store.SeedTag(member.Tag{ID: tagID, Name: "kept"})
	taggedID, _ := f.seedMember("tagged")
	f.store.SeedMemberTags(taggedID, tagID)
	err = failing.DeleteMemberTag(f.ctx(t), member.DeleteMemberTagCommand{ID: tagID})
	require.Equal(t, connect.CodeInternal, errs.Wrap(err).Code())
	require.Contains(t, f.store.Tags(), tagID)
	require.Equal(t, []string{tagID}, f.store.MemberTags(taggedID))
}

func TestMemberTagsRequireAdministrator(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	denyAdmin(f)

	_, err := f.service.ListMemberTags(f.ctx(t), member.ListMemberTagsQuery{})
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())
	_, err = f.service.CreateMemberTag(f.ctx(t), member.CreateMemberTagCommand{Name: "denied"})
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())
	require.Empty(t, f.store.Tags())
}

func TestListMemberTagsCountsMembersAndCapsTheLimit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tagID := uuid.NewString()
	f.store.SeedTag(member.Tag{ID: tagID, Name: "patrons"})
	f.store.SeedMemberTags(f.memberID, tagID)

	page, err := f.service.ListMemberTags(f.ctx(t), member.ListMemberTagsQuery{Limit: 1000, Offset: -3})

	require.NoError(t, err)
	require.Equal(t, member.Pagination{Total: 1, Limit: 500, Offset: 0, HasMore: false}, page.Pagination)
	require.Equal(t, []member.Tag{{ID: tagID, Name: "patrons", MemberCount: 1}}, page.Tags)
}
