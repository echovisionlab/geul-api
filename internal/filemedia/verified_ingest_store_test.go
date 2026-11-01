package filemedia_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func verifiedIngestFile() filemedia.File {
	return filemedia.File{ID: uuid.NewString(), FileName: "source.webp", MimeType: "image/webp", Extension: "webp", FileSize: 64, SHA256: bytes.Repeat([]byte{7}, 32), IngestSlotID: new(uuid.NewString()), IngestAttemptID: new(uuid.NewString())}
}

func TestVerifiedIngestStoresReceiptAuditAndExactRelationshipAfterFreshAuthority(t *testing.T) {
	f := newLibraryFixture(t)
	filemedia.ExportIngestChecker(f.files, f.checker.Client(t))
	file := verifiedIngestFile()
	err := filemedia.ExportCreateVerifiedFile(f.files, f.ctx, file, int32(managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE), 0, "", nil)
	require.NoError(t, err)
	stored := f.store.Files[file.ID]
	require.Equal(t, "source", stored.Row.Name)
	require.Equal(t, file.SHA256, stored.SHA256)
	require.Equal(t, new(f.user.MemberID.String()), stored.Row.MemberID)
	require.NotNil(t, stored.Row.ParentID)
	require.Len(t, f.store.Folders, 3)
	require.Equal(t, file.IngestAttemptID, stored.IngestAttemptID)
	require.Equal(t, "UPLOAD_TYPE_GENERAL_FILE", f.store.IngestBindings[file.ID].UploadType)
	require.Empty(t, f.store.IngestBindings[file.ID].EntityID)
	require.Len(t, f.audit.Records(), 1)
	require.Len(t, f.checker.Applied(), 1)
	apply, err := policyv1.File.TouchPolicy(file.ID)
	require.NoError(t, err)
	require.Equal(t, []policyv1.RelationshipMutation{apply}, f.checker.Applied()[0])
	require.Equal(t, []mutation.Step{mutation.StepAuthorizationLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepRelationships, mutation.StepCommit}, f.units.Steps())
	require.Equal(t, []string{f.user.IdentityID.String()}, f.fence.Locked())
}

func TestVerifiedIngestFailuresRestoreFileBindingAndFolders(t *testing.T) {
	for _, failure := range []string{"inactive", "denied", "checksum", "audit", "relationships"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			f := newLibraryFixture(t)
			filemedia.ExportIngestChecker(f.files, f.checker.Client(t))
			file := verifiedIngestFile()
			switch failure {
			case "inactive":
				f.fence.Deactivate(f.user.IdentityID.String())
			case "denied":
				f.checker.Deny(policyv1.File.ManageLibrary()).Deny(policyv1.File.List())
			case "checksum":
				file.SHA256 = []byte{1}
			case "audit":
				f.audit.Err = errors.New("audit failed")
			case "relationships":
				f.checker.FailWrites(errors.New("relationship write failed"))
			}
			err := filemedia.ExportCreateVerifiedFile(f.files, f.ctx, file, int32(managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE), 0, "", nil)
			require.Error(t, err)
			require.Empty(t, f.store.Files)
			require.Empty(t, f.store.IngestBindings)
			require.Empty(t, f.store.Folders)
			require.Empty(t, f.checker.Applied())
			require.Equal(t, mutation.StepRollback, f.units.Steps()[len(f.units.Steps())-1])
		})
	}
}

func TestVerifiedMultipartIngestRejectsChangedStoredAuthorityBeforeCreation(t *testing.T) {
	for _, state := range []string{"upload type", "status", "File", "missing"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			f := newLibraryFixture(t)
			filemedia.ExportIngestChecker(f.files, f.checker.Client(t))
			file := verifiedIngestFile()
			expected := domain.UploadSession{UploadID: uuid.NewString(), FileID: file.ID, UploadType: "UPLOAD_TYPE_EDITOR_IMAGE", Status: domain.UploadSessionStatusFinalizing}
			stored := expected
			switch state {
			case "upload type":
				stored.UploadType = "UPLOAD_TYPE_EDITOR_AUDIO"
			case "status":
				stored.Status = domain.UploadSessionStatusAborted
			case "File":
				stored.FileID = uuid.NewString()
			}
			if state != "missing" {
				f.store.UploadSessions[stored.UploadID] = stored
			}
			err := filemedia.ExportCreateVerifiedFile(f.files, f.ctx, file, int32(managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE), 0, "", &expected)
			require.Error(t, err)
			var outcome *domainerr.Error
			require.ErrorAs(t, err, &outcome)
			require.Equal(t, domainerr.FailedPrecondition, outcome.Kind)
			require.Empty(t, f.store.Files)
			require.Empty(t, f.audit.Records())
			require.Empty(t, f.checker.Applied())
			require.Empty(t, f.fence.Locked())
		})
	}
}

func TestVerifiedIngestDelegatesMaskedPostAuthorizationToOwnerBeforeFileState(t *testing.T) {
	f := newLibraryFixture(t)
	filemedia.ExportIngestChecker(f.files, f.checker.Client(t))
	owner := uuid.NewString()
	denied := domainerr.ResourceNotFound("post", owner)
	f.store.PolicyDenied["post\x00"+owner] = denied
	err := filemedia.ExportCreateVerifiedFile(f.files, f.ctx, verifiedIngestFile(), int32(managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE), 0, owner, nil)
	require.ErrorIs(t, err, denied)
	require.Len(t, f.store.PolicyChecks, 1)
	require.Equal(t, owner, f.store.PolicyChecks[0].ID)
	require.True(t, f.store.PolicyChecks[0].Edit)
	require.Empty(t, f.store.Files)
	require.Empty(t, f.fence.Locked())
}
