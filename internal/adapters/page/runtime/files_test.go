package runtime

import (
	"context"
	"testing"

	filemediadomain "github.com/echovisionlab/geul-api/internal/filemedia"

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
) (*filemediadomain.Delivery, error) {
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
