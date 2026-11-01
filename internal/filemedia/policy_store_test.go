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
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func policyFixture(t *testing.T) (*libraryFixture, domain.PolicyRelation, filemedia.UpdateFileDownloadPolicyCommand) {
	t.Helper()
	f := newLibraryFixture(t)
	row := filemediatest.CatalogFile("policy", nil)
	f.store.Files[row.ID] = filemediatest.CatalogFileRecord{Row: row}
	selector := domain.PolicySelector{OwnerKind: "post", EntityID: uuid.NewString(), BlockID: new(uuid.NewString()), ReferencePath: new("file")}
	relation := domain.PolicyRelation{Selector: selector, FileID: row.ID, Audience: mediaasset.FileDownloadAudienceDisabled, ResourceType: "post", ResourceID: selector.EntityID}
	f.store.Policies[filemediatest.PolicyKey(selector)] = filemediatest.PolicyRecord{Relation: relation}
	command := filemedia.UpdateFileDownloadPolicyCommand{Target: filemedia.FileDownloadPolicyTarget{EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, EntityID: selector.EntityID, BlockID: selector.BlockID, ReferencePath: selector.ReferencePath}, ExpectedFileID: row.ID, Policy: filemedia.FileDownloadPolicyState{Audience: managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_DISABLED}, Observed: &filemedia.FileDownloadPolicyState{Audience: managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_DISABLED}}
	return f, relation, command
}
func TestFilePolicyStoreMergesIndependentSegmentChanges(t *testing.T) {
	f, relation, command := policyFixture(t)
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	names := []string{"Alpha", "Beta", "Gamma"}
	for i, id := range ids {
		f.store.Segments[id] = domain.PolicySegment{ID: id, Name: names[i], Kind: "SEGMENT_TYPE_MEMBER_TAGS", Eligible: true}
	}
	relation.Audience = mediaasset.FileDownloadAudienceRestricted
	f.store.Policies[filemediatest.PolicyKey(relation.Selector)] = filemediatest.PolicyRecord{Relation: relation, SegmentIDs: ids[:2]}
	command.Observed = &filemedia.FileDownloadPolicyState{Audience: managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED, AudienceSegmentIDs: ids[:1]}
	command.Policy = filemedia.FileDownloadPolicyState{Audience: managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED, AudienceSegmentIDs: []string{ids[0], ids[2]}}
	got, err := f.files.UpdateFileDownloadPolicy(f.ctx, command)
	require.NoError(t, err)
	require.Equal(t, managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED, got.Audience)
	require.Len(t, got.AudienceSegments, 3)
	for i, summary := range got.AudienceSegments {
		require.Equal(t, ids[i], summary.Id)
	}
	stored := f.store.Policies[filemediatest.PolicyKey(relation.Selector)]
	sorted := slices.Sorted(slices.Values(ids))
	require.Equal(t, sorted, stored.SegmentIDs)
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
func TestFilePolicyOwnerDenialPrecedesSegmentValidation(t *testing.T) {
	f, relation, command := policyFixture(t)
	denied := domainerr.Denied("owner denied")
	f.store.PolicyDenied["post\x00"+relation.ResourceID] = denied
	command.Policy = filemedia.FileDownloadPolicyState{Audience: managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED, AudienceSegmentIDs: []string{uuid.NewString()}}
	_, err := f.files.UpdateFileDownloadPolicy(f.ctx, command)
	require.ErrorIs(t, err, denied)
	require.Empty(t, f.store.ValidatedPolicySegments)
	require.Zero(t, f.store.Writes)
	require.Empty(t, f.audit.Records())
}
func TestFilePolicyChangedAttachmentAndSignalFailureRollBack(t *testing.T) {
	for _, mode := range []string{"attachment", "signal"} {
		t.Run(mode, func(t *testing.T) {
			f, relation, command := policyFixture(t)
			command.Policy.Audience = managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_PUBLIC
			if mode == "attachment" {
				command.ExpectedFileID = uuid.NewString()
			} else {
				f.store.PolicySignalError = errors.New("signal failed")
			}
			_, err := f.files.UpdateFileDownloadPolicy(f.ctx, command)
			require.Error(t, err)
			require.Equal(t, mediaasset.FileDownloadAudienceDisabled, f.store.Policies[filemediatest.PolicyKey(relation.Selector)].Relation.Audience)
			require.Zero(t, f.store.Writes)
			require.Empty(t, f.store.PolicySignals)
		})
	}
}
func TestFilePolicyReadRejectsIneligibleCurrentSegment(t *testing.T) {
	f, relation, command := policyFixture(t)
	id := uuid.NewString()
	f.store.Segments[id] = domain.PolicySegment{ID: id, Name: "all", Kind: "SEGMENT_TYPE_ALL_MEMBERS", Eligible: false}
	relation.Audience = mediaasset.FileDownloadAudienceRestricted
	f.store.Policies[filemediatest.PolicyKey(relation.Selector)] = filemediatest.PolicyRecord{Relation: relation, SegmentIDs: []string{id}}
	got, err := f.files.GetFileDownloadPolicy(f.ctx, filemedia.GetFileDownloadPolicyQuery{Target: command.Target})
	require.Nil(t, got)
	require.ErrorContains(t, err, "attachment references an ineligible audience segment")
	require.Len(t, f.store.PolicyChecks, 1)
	require.False(t, f.store.PolicyChecks[0].Edit)
}

func TestPolicySegmentsDeduplicateAndRejectInvalidRequests(t *testing.T) {
	f, relation, command := policyFixture(t)
	command.Policy.Audience = managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED
	got, err := f.files.UpdateFileDownloadPolicy(f.ctx, command)
	require.NoError(t, err)
	require.Empty(t, got.AudienceSegments)
	require.Len(t, f.store.PolicySignals, 1)
	command.Observed.Audience = managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED
	_, err = f.files.UpdateFileDownloadPolicy(f.ctx, command)
	require.NoError(t, err)
	require.Len(t, f.store.PolicySignals, 1)
	id := uuid.NewString()
	f.store.Segments[id] = domain.PolicySegment{ID: id, Name: "Members", Kind: "SEGMENT_TYPE_MEMBER_TAGS", Eligible: true}
	command.Policy.AudienceSegmentIDs = []string{id, id}
	got, err = f.files.UpdateFileDownloadPolicy(f.ctx, command)
	require.NoError(t, err)
	require.Len(t, got.AudienceSegments, 1)
	require.Equal(t, id, got.AudienceSegments[0].Id)
	require.Equal(t, managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS, got.AudienceSegments[0].SegmentType)
	command.Observed.AudienceSegmentIDs = []string{id}
	command.Policy.Audience = managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_PUBLIC
	_, err = f.files.UpdateFileDownloadPolicy(f.ctx, command)
	require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
	command.Policy.Audience = managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED
	command.Policy.AudienceSegmentIDs = nil
	for i := 0; i < 21; i++ {
		command.Policy.AudienceSegmentIDs = append(command.Policy.AudienceSegmentIDs, uuid.NewString())
	}
	_, err = f.files.UpdateFileDownloadPolicy(f.ctx, command)
	require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
	require.Equal(t, mediaasset.FileDownloadAudienceRestricted, f.store.Policies[filemediatest.PolicyKey(relation.Selector)].Relation.Audience)
}
func TestPolicyUsesCurrentRelationAndIgnoresIngestProvenance(t *testing.T) {
	f, relation, command := policyFixture(t)
	f.store.IngestBindings[relation.FileID] = domain.IngestBinding{FileID: relation.FileID, UploadType: "UPLOAD_TYPE_EDITOR_AUDIO", EntityType: new("TRANSCODE_ENTITY_TYPE_POST"), EntityID: uuid.NewString()}
	command.Policy.Audience = managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_PUBLIC
	got, err := f.files.UpdateFileDownloadPolicy(f.ctx, command)
	require.NoError(t, err)
	require.Equal(t, relation.ResourceID, got.EntityId)
	require.Equal(t, managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_PUBLIC, got.Audience)
}
