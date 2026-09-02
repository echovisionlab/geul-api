package filemedia

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type recordingPagePolicyAccess struct {
	lockedViewDB *gorm.DB
	lockedEditDB *gorm.DB
	lockedViewID string
	lockedEditID string
	viewCalls    int
	editCalls    int
}

func (a *recordingPagePolicyAccess) RequireLockedView(_ context.Context, tx *gorm.DB, pageID string) error {
	a.viewCalls++
	a.lockedViewDB = tx
	a.lockedViewID = pageID
	return nil
}

func (a *recordingPagePolicyAccess) RequireLockedEdit(_ context.Context, tx *gorm.DB, pageID string) error {
	a.editCalls++
	a.lockedEditDB = tx
	a.lockedEditID = pageID
	return nil
}

func TestPageFilePolicyUsesOwningLockedAccess(t *testing.T) {
	t.Parallel()
	db := newPostAccessUnitDB(t)
	pageID := "11111111-1111-4111-8111-111111111111"

	viewAccess := &recordingPagePolicyAccess{}
	viewService := &FileService{pagePolicyAccess: viewAccess}
	require.NoError(t, viewService.authorizeFileDownloadPolicyOwner(
		t.Context(), db,
		fileDownloadPolicyRelation{ResourceType: "page", ResourceID: pageID},
		filePolicyOwnerView,
	))
	require.Equal(t, 1, viewAccess.viewCalls)
	require.Zero(t, viewAccess.editCalls)
	require.Same(t, db, viewAccess.lockedViewDB)
	require.Equal(t, pageID, viewAccess.lockedViewID)

	editAccess := &recordingPagePolicyAccess{}
	editService := &FileService{pagePolicyAccess: editAccess}
	require.NoError(t, editService.authorizeFileDownloadPolicyOwner(
		t.Context(), db,
		fileDownloadPolicyRelation{ResourceType: "page", ResourceID: pageID},
		filePolicyOwnerEdit,
	))
	require.Equal(t, 1, editAccess.editCalls)
	require.Zero(t, editAccess.viewCalls)
	require.Same(t, db, editAccess.lockedEditDB)
	require.Equal(t, pageID, editAccess.lockedEditID)
}
