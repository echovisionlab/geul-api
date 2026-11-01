package filemedia_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	telemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type folderMembers struct{}

func (folderMembers) Load(context.Context, []string) (map[string]*commonv1.MemberSummary, error) {
	return map[string]*commonv1.MemberSummary{}, nil
}

type libraryFixture struct {
	store   *filemediatest.LibraryStore
	units   *mutationtest.UnitOfWork[filemedia.Store]
	checker *authztest.Checker
	fence   *identitystatetest.Fence
	audit   *audittest.Recorder
	files   *filemedia.Service
	ctx     context.Context
	user    *auth.UserInfo
}

func newLibraryFixture(t *testing.T) *libraryFixture {
	t.Helper()
	store := filemediatest.NewLibraryStore()
	units := mutationtest.New[filemedia.Store](store)
	checker := authztest.AllowAll()
	fence := &identitystatetest.Fence{}
	audit := &audittest.Recorder{}
	user := &auth.UserInfo{IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(uuid.NewString()), SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true}
	request, err := telemetry.NewPropagatedRequestContext(uuid.NewString(), telemetry.MemberActor{IdentityID: user.IdentityID.String(), MemberID: user.MemberID.String(), SessionID: user.SessionID.String()})
	require.NoError(t, err)
	ctx := auth.WithUser(telemetry.WithRequestContext(t.Context(), request), user)
	return &libraryFixture{store: store, units: units, checker: checker, fence: fence, audit: audit, files: filemedia.ExportLibraryService(units, checker, fence, audit, folderMembers{}), ctx: ctx, user: user}
}
func TestLibraryFolderMutationsUseStoreAndAudit(t *testing.T) {
	f := newLibraryFixture(t)
	root, err := f.files.CreateFileFolder(f.ctx, filemedia.CreateFileFolderCommand{Name: " Root "})
	require.NoError(t, err)
	require.Equal(t, "Root", root.Name)
	stored := f.store.Folders[root.Id]
	require.Equal(t, f.user.MemberID.String(), *stored.CreatedByMemberID)
	require.Len(t, f.audit.Records(), 1)
	child, err := f.files.CreateFileFolder(f.ctx, filemedia.CreateFileFolderCommand{ParentID: root.Id, Name: "Child"})
	require.NoError(t, err)
	require.Equal(t, root.Id, child.GetParentId())
	renamed, err := f.files.RenameFileFolder(f.ctx, filemedia.RenameFileFolderCommand{FolderID: child.Id, Name: "Renamed"})
	require.NoError(t, err)
	require.Equal(t, "Renamed", renamed.Name)
	moved, err := f.files.MoveFileFolder(f.ctx, filemedia.MoveFileFolderCommand{FolderID: child.Id})
	require.NoError(t, err)
	require.Nil(t, moved.ParentId)
	require.Nil(t, f.store.Folders[child.Id].ParentID)
	require.Len(t, f.audit.Records(), 4)
	require.Equal(t, []string{f.user.IdentityID.String(), f.user.IdentityID.String(), f.user.IdentityID.String(), f.user.IdentityID.String()}, f.fence.Locked())
}
func TestLibraryNoOpDoesNotWriteOrAudit(t *testing.T) {
	f := newLibraryFixture(t)
	folder, err := f.files.CreateFileFolder(f.ctx, filemedia.CreateFileFolderCommand{Name: "same"})
	require.NoError(t, err)
	writes := f.store.Writes
	_, err = f.files.RenameFileFolder(f.ctx, filemedia.RenameFileFolderCommand{FolderID: folder.Id, Name: "same"})
	require.NoError(t, err)
	_, err = f.files.MoveFileFolder(f.ctx, filemedia.MoveFileFolderCommand{FolderID: folder.Id})
	require.NoError(t, err)
	require.Equal(t, writes, f.store.Writes)
	require.Len(t, f.audit.Records(), 1)
}
func TestLibraryAuthorizationAndAuditFailuresRollback(t *testing.T) {
	for _, mode := range []string{"permission", "inactive", "audit"} {
		t.Run(mode, func(t *testing.T) {
			f := newLibraryFixture(t)
			switch mode {
			case "permission":
				f.checker.Deny(policyv1.File.ManageLibrary())
			case "inactive":
				f.fence.Deactivate(f.user.IdentityID.String())
			case "audit":
				f.audit.Err = errors.New("audit failed")
			}
			_, err := f.files.CreateFileFolder(f.ctx, filemedia.CreateFileFolderCommand{Name: "blocked"})
			require.Error(t, err)
			require.Empty(t, f.store.Folders)
			require.Zero(t, f.store.Writes)
			steps := f.units.Steps()
			require.Equal(t, mutation.StepRollback, steps[len(steps)-1])
			if mode != "audit" {
				require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
				require.Empty(t, f.audit.Records())
			}
		})
	}
}
func TestLibraryCycleAndDuplicateNameProtection(t *testing.T) {
	f := newLibraryFixture(t)
	root, err := f.files.CreateFileFolder(f.ctx, filemedia.CreateFileFolderCommand{Name: "root"})
	require.NoError(t, err)
	child, err := f.files.CreateFileFolder(f.ctx, filemedia.CreateFileFolderCommand{ParentID: root.Id, Name: "child"})
	require.NoError(t, err)
	_, err = f.files.MoveFileFolder(f.ctx, filemedia.MoveFileFolderCommand{FolderID: root.Id, ParentID: child.Id})
	require.Error(t, err)
	require.Nil(t, f.store.Folders[root.Id].ParentID)
	_, err = f.files.CreateFileFolder(f.ctx, filemedia.CreateFileFolderCommand{Name: "root"})
	require.Error(t, err)
	require.Len(t, f.store.Folders, 2)
	require.Len(t, f.audit.Records(), 2)
}
func TestLibraryMissingFolderKeepsExistingPreflightOrder(t *testing.T) {
	f := newLibraryFixture(t)
	f.checker.Deny(policyv1.File.ManageLibrary())
	_, err := f.files.RenameFileFolder(f.ctx, filemedia.RenameFileFolderCommand{FolderID: uuid.NewString(), Name: "other"})
	require.Error(t, err)
	require.Empty(t, f.checker.Checks())
	require.Empty(t, f.fence.Locked())
	require.Empty(t, f.audit.Records())
}
