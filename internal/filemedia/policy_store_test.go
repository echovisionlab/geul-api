package filemedia_test

import (
	"errors"
	"slices"
	"testing"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func policyFixture(t *testing.T) (*libraryFixture, domain.PolicyRelation, filemedia.UpdateFileDownloadPolicyCommand) {
	t.Helper()
	f := newLibraryFixture(t)
	row := filemediatest.CatalogFile("policy", nil)
	f.store.Files[row.ID] = filemediatest.CatalogFileRecord{Row: row}
	selector := domain.PolicySelector{OwnerKind: "post", EntityID: uuid.NewString(), BlockID: new(uuid.NewString()), ReferencePath: new("file")}
	relation := domain.PolicyRelation{Selector: selector, FileID: row.ID, Access: mediaasset.FileDownloadAccessDisabled, ResourceType: "post", ResourceID: selector.EntityID}
	f.store.Policies[filemediatest.PolicyKey(selector)] = filemediatest.PolicyRecord{Relation: relation}
	command := filemedia.UpdateFileDownloadPolicyCommand{Target: filemedia.FileDownloadPolicyTarget{EntityType: filemedia.EntityPost, EntityID: selector.EntityID, BlockID: selector.BlockID, ReferencePath: selector.ReferencePath}, ExpectedFileID: row.ID, Policy: filemedia.FileDownloadPolicyState{Access: filemedia.DownloadAccessDisabled}, Observed: &filemedia.FileDownloadPolicyState{Access: filemedia.DownloadAccessDisabled}}
	return f, relation, command
}
func TestFilePolicyStoreMergesIndependentCriteriaChanges(t *testing.T) {
	f, relation, command := policyFixture(t)
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	names := []string{"Alpha", "Beta", "Gamma"}
	for i, id := range ids {
		f.store.CriteriaSet[id] = domain.PolicyCriteria{ID: id, Name: names[i], Kind: "MEMBER_CRITERIA_KIND_MEMBER_TAGS", Eligible: true}
	}
	relation.Access = mediaasset.FileDownloadAccessRestricted
	f.store.Policies[filemediatest.PolicyKey(relation.Selector)] = filemediatest.PolicyRecord{Relation: relation, CriteriaIDs: ids[:2]}
	command.Observed = &filemedia.FileDownloadPolicyState{Access: filemedia.DownloadAccessRestricted, MemberCriteriaIDs: ids[:1]}
	command.Policy = filemedia.FileDownloadPolicyState{Access: filemedia.DownloadAccessRestricted, MemberCriteriaIDs: []string{ids[0], ids[2]}}
	got, err := f.files.UpdateFileDownloadPolicy(f.ctx, command)
	require.NoError(t, err)
	require.Equal(t, mediaasset.FileDownloadAccessRestricted, got.Access)
	require.Len(t, got.MemberCriteria, 3)
	for i, summary := range got.MemberCriteria {
		require.Equal(t, ids[i], summary.ID)
	}
	stored := f.store.Policies[filemediatest.PolicyKey(relation.Selector)]
	sorted := slices.Sorted(slices.Values(ids))
	require.Equal(t, sorted, stored.CriteriaIDs)
	require.Len(t, f.audit.Records(), 1)
	require.Len(t, f.store.PolicySignals, 1)
	require.Equal(t, relation.ResourceID, f.store.PolicySignals[0].ResourceID)
	require.Equal(t, "post", f.store.PolicySignals[0].ResourceType)
	require.Len(t, f.store.PolicyChecks, 2)
	for _, check := range f.store.PolicyChecks {
		require.True(t, check.Edit)
	}
}
func TestFilePolicyNoOpDoesNotAuditOrSignal(t *testing.T) {
	f, _, command := policyFixture(t)
	_, err := f.files.UpdateFileDownloadPolicy(f.ctx, command)
	require.NoError(t, err)
	require.Zero(t, f.store.Writes)
	require.Empty(t, f.audit.Records())
	require.Empty(t, f.store.PolicySignals)
}
func TestFilePolicyOwnerDenialPrecedesCriteriaValidation(t *testing.T) {
	f, relation, command := policyFixture(t)
	denied := domainerr.Denied("owner denied")
	f.store.PolicyDenied["post\x00"+relation.ResourceID] = denied
	command.Policy = filemedia.FileDownloadPolicyState{Access: filemedia.DownloadAccessRestricted, MemberCriteriaIDs: []string{uuid.NewString()}}
	_, err := f.files.UpdateFileDownloadPolicy(f.ctx, command)
	require.ErrorIs(t, err, denied)
	require.Empty(t, f.store.ValidatedPolicyCriteriaSet)
	require.Zero(t, f.store.Writes)
	require.Empty(t, f.audit.Records())
}
func TestFilePolicyChangedAttachmentAndSignalFailureRollBack(t *testing.T) {
	for _, mode := range []string{"attachment", "signal"} {
		t.Run(mode, func(t *testing.T) {
			f, relation, command := policyFixture(t)
			command.Policy.Access = filemedia.DownloadAccessPublic
			if mode == "attachment" {
				command.ExpectedFileID = uuid.NewString()
			} else {
				f.store.PolicySignalError = errors.New("signal failed")
			}
			_, err := f.files.UpdateFileDownloadPolicy(f.ctx, command)
			require.Error(t, err)
			require.Equal(t, mediaasset.FileDownloadAccessDisabled, f.store.Policies[filemediatest.PolicyKey(relation.Selector)].Relation.Access)
			require.Zero(t, f.store.Writes)
			require.Empty(t, f.store.PolicySignals)
		})
	}
}
func TestFilePolicyReadRejectsIneligibleCurrentCriteria(t *testing.T) {
	f, relation, command := policyFixture(t)
	id := uuid.NewString()
	f.store.CriteriaSet[id] = domain.PolicyCriteria{ID: id, Name: "all", Kind: "MEMBER_CRITERIA_KIND_ALL_MEMBERS", Eligible: false}
	relation.Access = mediaasset.FileDownloadAccessRestricted
	f.store.Policies[filemediatest.PolicyKey(relation.Selector)] = filemediatest.PolicyRecord{Relation: relation, CriteriaIDs: []string{id}}
	got, err := f.files.GetFileDownloadPolicy(f.ctx, filemedia.GetFileDownloadPolicyQuery{Target: command.Target})
	require.Nil(t, got)
	require.ErrorContains(t, err, "attachment references an ineligible Member criteria")
	require.Len(t, f.store.PolicyChecks, 1)
	require.False(t, f.store.PolicyChecks[0].Edit)
}

func TestPolicyCriteriaSetDeduplicateAndRejectInvalidRequests(t *testing.T) {
	f, relation, command := policyFixture(t)
	command.Policy.Access = filemedia.DownloadAccessRestricted
	got, err := f.files.UpdateFileDownloadPolicy(f.ctx, command)
	require.NoError(t, err)
	require.Empty(t, got.MemberCriteria)
	require.Len(t, f.store.PolicySignals, 1)
	command.Observed.Access = filemedia.DownloadAccessRestricted
	_, err = f.files.UpdateFileDownloadPolicy(f.ctx, command)
	require.NoError(t, err)
	require.Len(t, f.store.PolicySignals, 1)
	id := uuid.NewString()
	f.store.CriteriaSet[id] = domain.PolicyCriteria{ID: id, Name: "Members", Kind: "MEMBER_CRITERIA_KIND_MEMBER_TAGS", Eligible: true}
	command.Policy.MemberCriteriaIDs = []string{id, id}
	got, err = f.files.UpdateFileDownloadPolicy(f.ctx, command)
	require.NoError(t, err)
	require.Len(t, got.MemberCriteria, 1)
	require.Equal(t, id, got.MemberCriteria[0].ID)
	require.Equal(t, "MEMBER_CRITERIA_KIND_MEMBER_TAGS", got.MemberCriteria[0].Kind)
	command.Observed.MemberCriteriaIDs = []string{id}
	command.Policy.Access = filemedia.DownloadAccessPublic
	_, err = f.files.UpdateFileDownloadPolicy(f.ctx, command)
	require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
	command.Policy.Access = filemedia.DownloadAccessRestricted
	command.Policy.MemberCriteriaIDs = nil
	for i := 0; i < 21; i++ {
		command.Policy.MemberCriteriaIDs = append(command.Policy.MemberCriteriaIDs, uuid.NewString())
	}
	_, err = f.files.UpdateFileDownloadPolicy(f.ctx, command)
	require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
	require.Equal(t, mediaasset.FileDownloadAccessRestricted, f.store.Policies[filemediatest.PolicyKey(relation.Selector)].Relation.Access)
}
func TestPolicyUsesCurrentRelationAndIgnoresIngestProvenance(t *testing.T) {
	f, relation, command := policyFixture(t)
	f.store.IngestBindings[relation.FileID] = domain.IngestBinding{FileID: relation.FileID, UploadType: "UPLOAD_TYPE_EDITOR_AUDIO", EntityType: new("TRANSCODE_ENTITY_TYPE_POST"), EntityID: uuid.NewString()}
	command.Policy.Access = filemedia.DownloadAccessPublic
	got, err := f.files.UpdateFileDownloadPolicy(f.ctx, command)
	require.NoError(t, err)
	require.Equal(t, relation.ResourceID, got.EntityID)
	require.Equal(t, mediaasset.FileDownloadAccessPublic, got.Access)
}
