package release

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestReleaseAIDocumentContentUpdatedEventCoversTranslationLifecycle(t *testing.T) {
	if event := releaseAIDocumentContentUpdatedEvent(AIDocumentMutation{}, AIDocumentMutationResult{}, "member"); event != nil {
		t.Fatalf("no-op event = %+v", event)
	}
	event := releaseAIDocumentContentUpdatedEvent(
		AIDocumentMutation{ReleaseID: "release", Locale: "en", ExpectedSource: "en", Batch: &contentblock.Batch{}},
		AIDocumentMutationResult{DocumentRevision: "revision", Changed: true},
		"member",
	)
	if event.GetEntityId() != "release" || event.GetDocumentRevision() != "revision" || len(event.GetContributorMemberIds()) != 1 || event.GetContributorMemberIds()[0] != "member" {
		t.Fatalf("event = %+v", event)
	}
	require.Equal(t, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI, event.Source)
	require.True(t, event.GetDocumentStateChanged())
	require.Equal(t, "en", event.GetLocale())
	require.True(t, event.GetLocaleExists())
	require.Nil(t, event.TargetRevision)

	targetRevision := "target-revision"
	target := releaseAIDocumentContentUpdatedEvent(
		AIDocumentMutation{ReleaseID: "release", Locale: "ko", ExpectedSource: "en", Batch: &contentblock.Batch{}},
		AIDocumentMutationResult{DocumentRevision: "revision", TargetRevision: &targetRevision, Changed: true},
		"member",
	)
	require.NotNil(t, target)
	require.False(t, target.GetDocumentStateChanged())
	require.Equal(t, "ko", target.GetLocale())
	require.True(t, target.GetLocaleExists())
	require.Equal(t, targetRevision, target.GetTargetRevision())

	deleted := releaseAIDocumentContentUpdatedEvent(
		AIDocumentMutation{ReleaseID: "release", Locale: "ko", ExpectedSource: "en", DeleteTranslation: true},
		AIDocumentMutationResult{DocumentRevision: "revision", Changed: true},
		"member",
	)
	require.NotNil(t, deleted)
	require.False(t, deleted.GetDocumentStateChanged())
	require.False(t, deleted.GetLocaleExists())
	require.Nil(t, deleted.TargetRevision)
	require.Nil(t, releaseAIDocumentContentUpdatedEvent(
		AIDocumentMutation{ReleaseID: "release", Locale: "ko", ExpectedSource: "en", Batch: &contentblock.Batch{}},
		AIDocumentMutationResult{DocumentRevision: "revision", Changed: true},
		"member",
	))
}
