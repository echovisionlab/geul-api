package label

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestBuildLabelContentUpdatedEventUsesExactLocaleFences(t *testing.T) {
	source := buildLabelContentUpdatedEvent(
		"label-1", []string{"content"}, "document-1", []string{"member-2", "member-1", "member-1"},
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI, "en", true, nil, true,
	)
	require.NotNil(t, source)
	require.True(t, source.GetDocumentStateChanged())
	require.Equal(t, "document-1", source.GetDocumentRevision())
	require.Equal(t, "document.content", source.GetChangedFields()[0].GetPath())
	require.Equal(t, []string{"member-1", "member-2"}, source.GetContributorMemberIds())

	targetRevision := "target-1"
	target := buildLabelContentUpdatedEvent(
		"label-1", []string{"content"}, "document-1", nil,
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB, "ko", true, &targetRevision, false,
	)
	require.NotNil(t, target)
	require.False(t, target.GetDocumentStateChanged())
	require.Equal(t, targetRevision, target.GetTargetRevision())

	deleted := buildLabelContentUpdatedEvent(
		"label-1", []string{"content"}, "document-1", nil,
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI, "ko", false, nil, false,
	)
	require.NotNil(t, deleted)
	require.False(t, deleted.GetLocaleExists())
	require.Nil(t, deleted.TargetRevision)

	require.Nil(t, buildLabelContentUpdatedEvent(
		"label-1", []string{"content"}, "document-1", nil,
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI, "ko", true, &targetRevision, true,
	))
}
