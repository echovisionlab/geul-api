package releasepostgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/release"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestContentUpdatedEventRendersTheReleaseSignal(t *testing.T) {
	manage := contentUpdatedEvent(release.ContentUpdate{
		ReleaseID: "release-1", Source: release.SourceManage,
		Fields: []release.ChangedField{{Path: "media.artwork", Kind: release.FieldMedia}, {Path: "settings.type", Kind: release.FieldConfiguration}},
	})
	require.Equal(t, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_RELEASE, manage.EntityType)
	require.Equal(t, "release-1", manage.EntityId)
	require.Equal(t, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE, manage.Source)
	require.Equal(t, []*managev1.ContentUpdatedField{
		{Path: "media.artwork", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_MEDIA},
		{Path: "settings.type", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
	}, manage.ChangedFields)
	require.Nil(t, manage.ContributorMemberIds)
	require.Nil(t, manage.Locale)
	require.NotZero(t, manage.TimestampMs)

	revision, locale, exists, target := "revision", "ko", true, "target"
	ai := contentUpdatedEvent(release.ContentUpdate{
		ReleaseID: "release-1", Source: release.SourceAI,
		Fields:               []release.ChangedField{{Path: "title", Kind: release.FieldText}, {Path: "state.status", Kind: release.FieldState}, {Path: "relations.labels", Kind: release.FieldRelation}},
		ContributorMemberIDs: []string{"member"}, DocumentRevision: &revision, Locale: &locale, LocaleExists: &exists,
		TargetRevision: &target,
	})
	require.Equal(t, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI, ai.Source)
	require.Equal(t, []managev1.ContentUpdatedFieldKind{
		managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT,
		managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE,
		managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION,
	}, []managev1.ContentUpdatedFieldKind{ai.ChangedFields[0].Kind, ai.ChangedFields[1].Kind, ai.ChangedFields[2].Kind})
	require.Equal(t, []string{"member"}, ai.ContributorMemberIds)
	require.Equal(t, "revision", ai.GetDocumentRevision())
	require.Equal(t, "ko", ai.GetLocale())
	require.True(t, ai.GetLocaleExists())
	require.Equal(t, "target", ai.GetTargetRevision())
	require.False(t, ai.DocumentStateChanged)
}
