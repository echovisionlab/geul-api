package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type appliedContentUpdatePublisher struct {
	events       []*managev1.ContentUpdatedEvent
	executor     eventpkg.DBTX
	err          error
	lifecycleErr error
}

func (*appliedContentUpdatePublisher) EnqueueProtobuf(
	context.Context,
	string,
	string,
	proto.Message,
) error {
	return nil
}

func (*appliedContentUpdatePublisher) NotifyProtobuf(
	context.Context,
	string,
	proto.Message,
) error {
	return nil
}

func (*appliedContentUpdatePublisher) PublishTranslationGenerate(
	context.Context,
	*managev1.TranslationGenerateEvent,
) error {
	return nil
}

func (publisher *appliedContentUpdatePublisher) PublishTranslationLifecycle(
	context.Context,
	*managev1.TranslationLifecycleEvent,
) error {
	return publisher.lifecycleErr
}

func (publisher *appliedContentUpdatePublisher) PublishContentUpdatedWithExecutor(
	_ context.Context,
	executor eventpkg.DBTX,
	event *managev1.ContentUpdatedEvent,
) error {
	publisher.executor = executor
	publisher.events = append(publisher.events, event)
	return publisher.err
}

func TestBuildAppliedTranslationContentUpdatedUsesExactTargetFence(t *testing.T) {
	t.Parallel()

	memberID := uuid.NewString()
	documentRevision := uuid.NewString()
	targetRevision := "tr1_exact"
	now := time.Unix(1_700_000_300, 0).UTC()
	event, err := buildAppliedTranslationContentUpdated(
		&model.TranslationJob{
			EntityType: "campaign", EntityID: uuid.NewString(), TargetLocale: "ko",
			RequestedByMemberID: memberID,
		},
		AppliedTranslationTarget{
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
		&model.TranslationJob{
			EntityType: "post", EntityID: uuid.NewString(), TargetLocale: "ko",
			RequestedByMemberID: uuid.NewString(),
		},
		AppliedTranslationTarget{
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

	job := &model.TranslationJob{
		EntityType: "post", EntityID: uuid.NewString(), TargetLocale: "ko",
		RequestedByMemberID: uuid.NewString(),
	}
	for name, result := range map[string]AppliedTranslationTarget{
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
				&model.TranslationJob{
					EntityType: string(definition.Kind), EntityID: uuid.NewString(), TargetLocale: "ko",
					RequestedByMemberID: uuid.NewString(),
				},
				AppliedTranslationTarget{
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
				&model.TranslationJob{
					EntityType: "campaign", EntityID: uuid.NewString(), TargetLocale: "ko",
					RequestedByMemberID: requestedBy,
				},
				AppliedTranslationTarget{
					Changed: true, DocumentRevision: uuid.NewString(), TargetRevision: "tr1_exact",
				},
				time.Now(),
			)
			require.Error(t, err)
		})
	}
}

func TestAppliedTranslationContentUpdateNoopDoesNotPublish(t *testing.T) {
	t.Parallel()

	publisher := &appliedContentUpdatePublisher{}
	manager := &TranslationJobManager{publisher: publisher}
	require.NoError(t, manager.publishAppliedTranslationContentUpdated(
		context.Background(),
		nil,
		&model.TranslationJob{},
		AppliedTranslationTarget{},
		time.Now(),
	))
	require.Empty(t, publisher.events)
}

func TestAppliedTranslationContentUpdateFailureRollsBackMutationAndJobDeletion(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	job := seedTranslationRetryTestJob(
		t,
		db,
		translationJobStatusRunning,
		"transactional-content-update",
		uuid.NewString(),
	)
	require.NoError(t, db.Exec(`CREATE TABLE applied_translation_result (job_id TEXT PRIMARY KEY)`).Error)
	publisher := &appliedContentUpdatePublisher{err: errors.New("notify unavailable")}
	manager := &TranslationJobManager{publisher: publisher}

	err := db.Transaction(func(tx *gorm.DB) error {
		if insertErr := tx.Exec(
			`INSERT INTO applied_translation_result (job_id) VALUES (?)`,
			job.ID,
		).Error; insertErr != nil {
			return insertErr
		}
		if deleteErr := completeAppliedTranslationJob(t.Context(), tx, job.ID); deleteErr != nil {
			return deleteErr
		}
		return manager.publishAppliedTranslationContentUpdated(
			t.Context(),
			tx,
			job,
			AppliedTranslationTarget{
				Changed: true, DocumentRevision: uuid.NewString(), TargetRevision: "tr1_exact",
			},
			time.Now(),
		)
	})
	require.ErrorContains(t, err, "notify unavailable")
	require.NotNil(t, publisher.executor)
	require.Len(t, publisher.events, 1)

	var appliedCount int64
	require.NoError(t, db.Table("applied_translation_result").Count(&appliedCount).Error)
	require.Zero(t, appliedCount)
	require.NoError(t, db.First(&model.TranslationJob{}, "id = ?", job.ID).Error)
}
