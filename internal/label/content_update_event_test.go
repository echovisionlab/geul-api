package label

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestBuildLabelLocaleContentUpdatedEventUsesExactLocaleFences(t *testing.T) {
	source := buildLabelLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID:             "label-1",
		Fields:               []string{"content"},
		DocumentRevision:     "document-1",
		Contributors:         []string{"member-2", "member-1", "member-1"},
		Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		Locale:               "en",
		DocumentStateChanged: true,
	})
	require.NotNil(t, source)
	require.True(t, source.GetDocumentStateChanged())
	require.Equal(t, "document-1", source.GetDocumentRevision())
	require.Equal(t, "document.content", source.GetChangedFields()[0].GetPath())
	require.Equal(t, []string{"member-1", "member-2"}, source.GetContributorMemberIds())

	targetRevision := "target-1"
	target := buildLabelLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID:         "label-1",
		Fields:           []string{"content"},
		DocumentRevision: "document-1",
		Source:           managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
		Locale:           "ko",
		TargetRevision:   &targetRevision,
	})
	require.NotNil(t, target)
	require.False(t, target.GetDocumentStateChanged())
	require.Equal(t, targetRevision, target.GetTargetRevision())

	deleted := buildLabelLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID:         "label-1",
		Fields:           []string{"content"},
		DocumentRevision: "document-1",
		Source:           managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		Locale:           "ko",
		LocaleDeleted:    true,
	})
	require.NotNil(t, deleted)
	require.False(t, deleted.GetLocaleExists())
	require.Nil(t, deleted.TargetRevision)

	require.Nil(t, buildLabelLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID:             "label-1",
		Fields:               []string{"content"},
		DocumentRevision:     "document-1",
		Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		Locale:               "ko",
		TargetRevision:       &targetRevision,
		DocumentStateChanged: true,
	}))
}
