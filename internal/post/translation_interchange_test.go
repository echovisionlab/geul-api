package post

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type postTranslationInterchangeAuditCapture struct {
	record sharedtelemetry.AuditRecord
}

func TestPostInterchangeTargetStoragePreservesPatchUpsertsAndExplicitDeletes(t *testing.T) {
	upsertID, deleteID := uuid.NewString(), uuid.NewString()
	storage, err := postInterchangeTargetStorage(&translation.Candidate{
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{
			Locale: "ko",
			Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: upsertID,
				Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
					Props: &contentv1.ParagraphLocaleProps{},
				}},
			}},
		},
		ContentBlockLocaleDeletes: []string{deleteID},
	}, uuid.NewString())
	require.NoError(t, err)
	require.NotNil(t, storage)
	require.Len(t, storage.LocaleGroups, 1)
	require.Equal(t, "ko", storage.LocaleGroups[0].Locale)
	require.Len(t, storage.LocaleGroups[0].Upserts, 1)
	require.Equal(t, upsertID, storage.LocaleGroups[0].Upserts[0].BlockID)
	require.Equal(t, []string{deleteID}, storage.LocaleGroups[0].Deletes)
}

func (capture *postTranslationInterchangeAuditCapture) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	capture.record = record
	return nil
}

func TestPostTranslationInterchangeAuditIsExactMemberLocaleContent(t *testing.T) {
	memberID := uuid.NewString()
	for _, test := range []struct {
		name       string
		previously bool
		operation  sharedtelemetry.AuditItemOperation
	}{
		{name: "create", operation: sharedtelemetry.AuditItemOperationCreated},
		{name: "update", previously: true, operation: sharedtelemetry.AuditItemOperationUpdated},
	} {
		t.Run(test.name, func(t *testing.T) {
			capture := &postTranslationInterchangeAuditCapture{}
			require.NoError(t, appendPostTranslationInterchangeAudit(
				t.Context(), nil, capture, memberID, "post-1", "ko", test.previously,
			))
			require.NoError(t, capture.record.Validate())
			require.Equal(t, sharedtelemetry.AuditPostUpdated, capture.record.Action)
			require.Equal(t, "post", capture.record.TargetType)
			require.Equal(t, "post-1", capture.record.TargetID)
			require.Equal(t, memberID, capture.record.MemberID)
			require.Equal(t, sharedtelemetry.ActorKindMember, capture.record.Kind)
			require.Equal(t, []string{"locale_content"}, capture.record.ChangedFields)
			require.Equal(t, "ko", capture.record.Locale)
			require.Equal(t, test.operation, capture.record.ItemOperation)
			require.Empty(t, capture.record.ContributorMemberIDs)
		})
	}
}

func TestPostSourceLocaleContentAuditUsesOriginMember(t *testing.T) {
	memberID := uuid.NewString()
	capture := &postTranslationInterchangeAuditCapture{}
	require.NoError(t, appendPostMemberLocaleContentAudit(
		t.Context(), nil, capture, memberID, "post-1", "en",
		sharedtelemetry.AuditItemOperationUpdated,
	))
	require.NoError(t, capture.record.Validate())
	require.Equal(t, memberID, capture.record.MemberID)
	require.Equal(t, sharedtelemetry.ActorKindMember, capture.record.Kind)
	require.Equal(t, []string{"locale_content"}, capture.record.ChangedFields)
	require.Equal(t, "en", capture.record.Locale)
}
