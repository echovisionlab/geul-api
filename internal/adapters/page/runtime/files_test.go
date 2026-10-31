package runtime

import (
	"context"
	"testing"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/stretchr/testify/require"
)

type pageManageFilesFixture struct {
	pageID         string
	expectedFileID string
}

func (f *pageManageFilesFixture) ResolveAuthorizedPageFeaturedImage(
	_ context.Context,
	pageID string,
	expectedFileID string,
) (*commonv1.MediaDelivery, error) {
	f.pageID = pageID
	f.expectedFileID = expectedFileID
	return nil, nil
}

func TestFilesDelegatesPageManageCapabilities(t *testing.T) {
	fixture := &pageManageFilesFixture{}
	adapter := NewFiles(fixture)

	_, err := adapter.ResolveAuthorizedPageFeaturedImage(t.Context(), "page-1", "file-2")
	require.NoError(t, err)
	require.Equal(t, "page-1", fixture.pageID)
	require.Equal(t, "file-2", fixture.expectedFileID)
}
