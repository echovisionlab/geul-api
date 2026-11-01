package translationpostgres

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestBuildAppliedTranslationContentUpdatedUsesExactTargetFence(t *testing.T) {
	t.Parallel()

	memberID := uuid.NewString()
	documentRevision := uuid.NewString()
	targetRevision := "tr1_exact"
	now := time.Unix(1_700_000_300, 0).UTC()
	event, err := buildAppliedTranslationContentUpdated(
		application.Job{
			EntityType: "campaign", EntityID: uuid.NewString(), TargetLocale: "ko",
			RequestedByMemberID: memberID,
		},
		application.AppliedTranslationTarget{
			Changed: true, DocumentRevision: documentRevision, TargetRevision: targetRevision,
		},
		now,
	)
	require.NoError(t, err)
	require.Equal(t, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_CAMPAIGN, event.GetEntityType())
	require.Equal(t, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI, event.GetSource())
	require.Equal(t, documentRevision, event.GetDocumentRevision())
	require.Equal(t, "ko", event.GetLocale())
	require.True(t, event.GetLocaleExists())
	require.Equal(t, targetRevision, event.GetTargetRevision())
	require.False(t, event.GetDocumentStateChanged())
	require.Len(t, event.GetChangedFields(), 1)
	require.Equal(t, "locale_content", event.GetChangedFields()[0].GetPath())
	require.Equal(
		t,
		managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT,
		event.GetChangedFields()[0].GetKind(),
	)
	require.Equal(t, []string{memberID}, event.GetContributorMemberIds())
	require.Equal(t, now.UnixMilli(), event.GetTimestampMs())
}

func TestBuildAppliedTranslationContentUpdatedUsesSourceRoleAfterLocaleSwitch(t *testing.T) {
	t.Parallel()

	documentRevision := uuid.NewString()
	event, err := buildAppliedTranslationContentUpdated(
		application.Job{
			EntityType: "post", EntityID: uuid.NewString(), TargetLocale: "ko",
			RequestedByMemberID: uuid.NewString(),
		},
		application.AppliedTranslationTarget{
			Changed: true, DocumentRevision: documentRevision, DocumentStateChanged: true,
		},
		time.Now(),
	)
	require.NoError(t, err)
	require.Equal(t, documentRevision, event.GetDocumentRevision())
	require.True(t, event.GetDocumentStateChanged())
	require.Equal(t, "ko", event.GetLocale())
	require.True(t, event.GetLocaleExists())
	require.Nil(t, event.TargetRevision)
}

func TestBuildAppliedTranslationContentUpdatedRejectsMixedLocaleRoleFences(t *testing.T) {
	t.Parallel()

	job := application.Job{
		EntityType: "post", EntityID: uuid.NewString(), TargetLocale: "ko",
		RequestedByMemberID: uuid.NewString(),
	}
	for name, result := range map[string]application.AppliedTranslationTarget{
		"source with target revision": {
			Changed: true, DocumentRevision: uuid.NewString(), DocumentStateChanged: true,
			TargetRevision: "tr1_exact",
		},
		"target without target revision": {
			Changed: true, DocumentRevision: uuid.NewString(),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := buildAppliedTranslationContentUpdated(job, result, time.Now())
			require.Error(t, err)
		})
	}
}

func TestBuildAppliedTranslationContentUpdatedUsesCompleteTranslationCatalog(t *testing.T) {
	t.Parallel()

	for _, definition := range translation.Definitions() {
		definition := definition
		t.Run(string(definition.Kind), func(t *testing.T) {
			t.Parallel()

			event, err := buildAppliedTranslationContentUpdated(
				application.Job{
					EntityType: string(definition.Kind), EntityID: uuid.NewString(), TargetLocale: "ko",
					RequestedByMemberID: uuid.NewString(),
				},
				application.AppliedTranslationTarget{
					Changed: true, DocumentRevision: uuid.NewString(), TargetRevision: "tr1_exact",
				},
				time.Now(),
			)
			require.NoError(t, err)
			require.Equal(t, definition.ContentEntityType, event.GetEntityType())
			require.Equal(t, "locale_content", event.GetChangedFields()[0].GetPath())
		})
	}
}

func TestBuildAppliedTranslationContentUpdatedRequiresCanonicalRequesterMember(t *testing.T) {
	t.Parallel()

	for name, requestedBy := range map[string]string{
		"missing":       "",
		"non canonical": " " + uuid.NewString(),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := buildAppliedTranslationContentUpdated(
				application.Job{
					EntityType: "campaign", EntityID: uuid.NewString(), TargetLocale: "ko",
					RequestedByMemberID: requestedBy,
				},
				application.AppliedTranslationTarget{
					Changed: true, DocumentRevision: uuid.NewString(), TargetRevision: "tr1_exact",
				},
				time.Now(),
			)
			require.Error(t, err)
		})
	}
}

func TestSourceLocaleUpdatedEventUsesCompleteTranslationCatalog(t *testing.T) {
	for _, definition := range translation.Definitions() {
		event := buildTypedSourceLocaleUpdatedEvent(string(definition.Kind), "entity-1", "revision-1")
		if event == nil {
			t.Fatalf("buildTypedSourceLocaleUpdatedEvent(%q) returned nil", definition.Kind)
		}
		if event.EntityType != definition.ContentEntityType {
			t.Fatalf("buildTypedSourceLocaleUpdatedEvent(%q).entity_type = %v, want %v", definition.Kind, event.EntityType, definition.ContentEntityType)
		}
		if !event.DocumentStateChanged || event.DocumentRevision == nil || *event.DocumentRevision != "revision-1" {
			t.Fatalf("buildTypedSourceLocaleUpdatedEvent(%q) lost document revision fence: %#v", definition.Kind, event)
		}
	}
}

func TestSourceLocaleUpdatedEventRejectsUnknownDomain(t *testing.T) {
	if event := buildTypedSourceLocaleUpdatedEvent("unknown", "entity-1", "revision-1"); event != nil {
		t.Fatalf("unknown Translation domain produced event %#v", event)
	}
}
