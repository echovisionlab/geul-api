package release

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentblock"
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
	require.Equal(t, "release", event.ReleaseID)
	require.Equal(t, "revision", *event.DocumentRevision)
	require.Equal(t, []string{"member"}, event.ContributorMemberIDs)
	require.Equal(t, SourceAI, event.Source)
	require.True(t, event.DocumentStateChanged)
	require.Equal(t, "en", *event.Locale)
	require.True(t, *event.LocaleExists)
	require.Nil(t, event.TargetRevision)

	targetRevision := "target-revision"
	target := releaseAIDocumentContentUpdatedEvent(
		AIDocumentMutation{ReleaseID: "release", Locale: "ko", ExpectedSource: "en", Batch: &contentblock.Batch{}},
		AIDocumentMutationResult{DocumentRevision: "revision", TargetRevision: &targetRevision, Changed: true},
		"member",
	)
	require.NotNil(t, target)
	require.False(t, target.DocumentStateChanged)
	require.Equal(t, "ko", *target.Locale)
	require.True(t, *target.LocaleExists)
	require.Equal(t, targetRevision, *target.TargetRevision)

	deleted := releaseAIDocumentContentUpdatedEvent(
		AIDocumentMutation{ReleaseID: "release", Locale: "ko", ExpectedSource: "en", DeleteTranslation: true},
		AIDocumentMutationResult{DocumentRevision: "revision", Changed: true},
		"member",
	)
	require.NotNil(t, deleted)
	require.False(t, deleted.DocumentStateChanged)
	require.False(t, *deleted.LocaleExists)
	require.Nil(t, deleted.TargetRevision)
	require.Nil(t, releaseAIDocumentContentUpdatedEvent(
		AIDocumentMutation{ReleaseID: "release", Locale: "ko", ExpectedSource: "en", Batch: &contentblock.Batch{}},
		AIDocumentMutationResult{DocumentRevision: "revision", Changed: true},
		"member",
	))
}
