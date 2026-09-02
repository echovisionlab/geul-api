//go:build integration

package emailauthoring

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type emailLayoutLocaleAuditAppender struct {
	records []sharedtelemetry.AuditRecord
}

func (appender *emailLayoutLocaleAuditAppender) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	appender.records = append(appender.records, record)
	return nil
}

type emailLayoutLocaleReferences struct{}

func (emailLayoutLocaleReferences) TemplateDeliveryRunCounts(
	context.Context, *gorm.DB, []string,
) (map[string]int32, error) {
	return map[string]int32{}, nil
}

func (emailLayoutLocaleReferences) LayoutExternalReferenceCounts(
	context.Context, *gorm.DB, []string,
) (map[string]LayoutExternalReferenceCounts, error) {
	return map[string]LayoutExternalReferenceCounts{}, nil
}

func (emailLayoutLocaleReferences) RequireTemplateMutable(context.Context, *gorm.DB, string) error {
	return nil
}

func (emailLayoutLocaleReferences) RequireLayoutMutable(context.Context, *gorm.DB, string) error {
	return nil
}

func (emailLayoutLocaleReferences) DetachTemplateHistory(context.Context, *gorm.DB, string) error {
	return nil
}

func (emailLayoutLocaleReferences) DetachLayoutHistory(context.Context, *gorm.DB, string) error {
	return nil
}

func TestInternalEmailLayoutLocaleCollaborationIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	baseContext, spiceDB := testutil.IntegrationAdminContext(t, db)
	admin := auth.GetUser(baseContext)
	require.NotNil(t, admin)
	memberID := string(admin.MemberID)
	ctx := testutil.NewAuditContext(t, string(admin.IdentityID), memberID)
	store := testutil.NewEmailContentBlockStore(t, spiceDB)
	references := emailLayoutLocaleReferences{}
	auditAppender := &emailLayoutLocaleAuditAppender{}

	manageService := NewAuditedEmailLayoutService(
		db, "", "", auditAppender, spiceDB,
		WithEmailLayoutCampaignDeliveryReferences(references),
		WithEmailLayoutContentBlockStore(store),
	)
	created, err := manageService.CreateEmailLayout(ctx, connect.NewRequest(
		&managev1.CreateEmailLayoutRequest{
			Name: "Locale collaboration layout",
			Key:  "locale-collaboration-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
			HtmlContent: `<!doctype html><html><head><style>.content{color:red}</style></head><body>` +
				`<main data-shell="fixed"><h1>Source title</h1><p>Source body</p>{{content}}</main></body></html>`,
			SourceLocale: "en",
		},
	))
	require.NoError(t, err)
	layoutID := created.Msg.Id
	auditAppender.records = nil

	service := NewAuditedInternalEmailLayoutService(
		db, auditAppender,
		WithInternalEmailLayoutCheckpoints(testcollaboration.NewCheckpoints(db, spiceDB)),
		WithInternalEmailLayoutCampaignDeliveryReferences(references),
		WithInternalEmailLayoutContentBlockStore(store),
	)
	source, err := service.LoadDocument(ctx, connect.NewRequest(
		&intrav1.LoadEmailLayoutDocumentRequest{EmailLayoutId: layoutID, Locale: "en"},
	))
	require.NoError(t, err)
	require.Equal(t, "en", source.Msg.SourceLocale)
	require.Equal(t, "en", source.Msg.Locale)
	require.NotEmpty(t, source.Msg.DocumentRevision)
	require.Nil(t, source.Msg.TargetRevision)
	sourceUnits, err := emailutil.ExtractLayoutContentUnits(source.Msg.ContentHtml)
	require.NoError(t, err)
	require.Len(t, sourceUnits, 2)

	seededAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	require.NoError(t, emailutil.SeedLayoutTranslationEntryFromSource(
		ctx, db, layoutID, "ko", "en", seededAt,
	))
	target, err := service.LoadDocument(ctx, connect.NewRequest(
		&intrav1.LoadEmailLayoutDocumentRequest{EmailLayoutId: layoutID, Locale: "ko"},
	))
	require.NoError(t, err)
	require.Equal(t, source.Msg.DocumentRevision, target.Msg.DocumentRevision)
	require.NotNil(t, target.Msg.TargetRevision)
	require.Empty(t, target.Msg.ContentHtml)
	require.Empty(t, target.Msg.ContentText)
	require.Len(t, target.Msg.Units, len(sourceUnits))
	seededValues := emailLayoutLocaleValueMap(target.Msg.LocaleValues)
	require.Equal(t, map[string]string{
		sourceUnits[0].Handle: sourceUnits[0].SourceValue,
		sourceUnits[1].Handle: sourceUnits[1].SourceValue,
	}, seededValues, "explicit target creation copies every current source value")

	nextValues := map[string]string{
		sourceUnits[0].Handle: "수정 제목",
		sourceUnits[1].Handle: "수정 본문",
	}
	baseTargetRequest := intrav1.SaveEmailLayoutDocumentRequest{
		EmailLayoutId: layoutID, Locale: "ko",
		ExpectedDocumentRevision: target.Msg.DocumentRevision,
		ExpectedTargetRevision:   target.Msg.TargetRevision,
		LocaleValues:             emailLayoutLocaleValueMessages(nextValues),
	}
	_, err = service.SaveDocument(ctx, connect.NewRequest(&baseTargetRequest))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	multipleContributors := proto.Clone(&baseTargetRequest).(*intrav1.SaveEmailLayoutDocumentRequest)
	multipleContributors.ContributorMemberIds = []string{memberID, uuid.NewString()}
	_, err = service.SaveDocument(ctx, connect.NewRequest(multipleContributors))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Empty(t, auditAppender.records)

	acceptedTarget := proto.Clone(&baseTargetRequest).(*intrav1.SaveEmailLayoutDocumentRequest)
	acceptedTarget.ContributorMemberIds = []string{memberID}
	savedTarget, err := service.SaveDocument(ctx, connect.NewRequest(acceptedTarget))
	require.NoError(t, err)
	require.Equal(t, source.Msg.DocumentRevision, savedTarget.Msg.DocumentRevision)
	require.NotNil(t, savedTarget.Msg.TargetRevision)
	require.NotEqual(t, *target.Msg.TargetRevision, *savedTarget.Msg.TargetRevision)
	require.Len(t, auditAppender.records, 1)
	requireEmailLayoutLocaleAudit(t, auditAppender.records[0], memberID, "ko")

	noOpTarget := proto.Clone(acceptedTarget).(*intrav1.SaveEmailLayoutDocumentRequest)
	noOpTarget.ExpectedTargetRevision = savedTarget.Msg.TargetRevision
	repeatedTarget, err := service.SaveDocument(ctx, connect.NewRequest(noOpTarget))
	require.NoError(t, err)
	require.Equal(t, savedTarget.Msg.DocumentRevision, repeatedTarget.Msg.DocumentRevision)
	require.Equal(t, savedTarget.Msg.TargetRevision, repeatedTarget.Msg.TargetRevision)
	require.Len(t, auditAppender.records, 1, "semantic target no-op must not append Audit")

	storedTarget, err := emailutil.LoadLayoutTranslationEntry(ctx, db, layoutID, "ko")
	require.NoError(t, err)
	require.NotNil(t, storedTarget)
	storedValues, err := emailutil.ExtractLayoutStoredLocaleValues(*storedTarget.ContentHTML)
	require.NoError(t, err)
	require.Equal(t, nextValues, storedValues)
	require.True(t, storedTarget.UpdatedAt.After(seededAt))
	targetUpdatedAt := storedTarget.UpdatedAt

	staleTarget := proto.Clone(acceptedTarget).(*intrav1.SaveEmailLayoutDocumentRequest)
	_, err = service.SaveDocument(ctx, connect.NewRequest(staleTarget))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	unknownUnit := proto.Clone(noOpTarget).(*intrav1.SaveEmailLayoutDocumentRequest)
	unknownUnit.LocaleValues = append(unknownUnit.LocaleValues, &intrav1.EmailLayoutLocaleValue{
		Handle: "unknown-unit", Value: "invalid",
	})
	_, err = service.SaveDocument(ctx, connect.NewRequest(unknownUnit))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	missingTarget, err := service.LoadDocument(ctx, connect.NewRequest(
		&intrav1.LoadEmailLayoutDocumentRequest{EmailLayoutId: layoutID, Locale: "fr"},
	))
	require.NoError(t, err)
	require.Nil(t, missingTarget.Msg.TargetRevision)
	_, err = service.SaveDocument(ctx, connect.NewRequest(
		&intrav1.SaveEmailLayoutDocumentRequest{
			EmailLayoutId: layoutID, Locale: "fr",
			ExpectedDocumentRevision: missingTarget.Msg.DocumentRevision,
			ContributorMemberIds:     []string{memberID},
		},
	))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err),
		"Collaboration cannot implicitly create a missing target")

	sparseValues := map[string]string{sourceUnits[0].Handle: "Deutscher Titel"}
	sparseHTML, sparseText, err := emailutil.ApplyLayoutLocaleValues(source.Msg.ContentHtml, sparseValues)
	require.NoError(t, err)
	sparseUpdatedAt := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, emailutil.UpsertLayoutTranslationEntry(
		ctx, db, layoutID, "de",
		translation.EntryWrite{ContentHTML: sparseHTML, ContentText: sparseText, Now: sparseUpdatedAt},
	))
	staleSparseTarget, err := service.LoadDocument(ctx, connect.NewRequest(
		&intrav1.LoadEmailLayoutDocumentRequest{EmailLayoutId: layoutID, Locale: "de"},
	))
	require.NoError(t, err)
	require.Empty(t, staleSparseTarget.Msg.ContentHtml)
	require.Equal(t, sparseValues, emailLayoutLocaleValueMap(staleSparseTarget.Msg.LocaleValues))
	require.Len(t, staleSparseTarget.Msg.Units, len(sourceUnits))

	changedSourceHTML := strings.NewReplacer(
		"Source title", "Changed source title",
		"Source body", "Changed source body",
	).Replace(source.Msg.ContentHtml)
	savedSource, err := service.SaveDocument(ctx, connect.NewRequest(
		&intrav1.SaveEmailLayoutDocumentRequest{
			EmailLayoutId: layoutID, Locale: "en",
			ExpectedDocumentRevision: source.Msg.DocumentRevision,
			ContributorMemberIds:     []string{memberID},
			ContentHtml:              changedSourceHTML,
		},
	))
	require.NoError(t, err)
	require.NotEqual(t, source.Msg.DocumentRevision, savedSource.Msg.DocumentRevision)
	require.Nil(t, savedSource.Msg.TargetRevision)
	require.Len(t, auditAppender.records, 2)
	requireEmailLayoutLocaleAudit(t, auditAppender.records[1], memberID, "en")

	_, err = service.SaveDocument(ctx, connect.NewRequest(
		&intrav1.SaveEmailLayoutDocumentRequest{
			EmailLayoutId: layoutID, Locale: "de",
			ExpectedDocumentRevision: staleSparseTarget.Msg.DocumentRevision,
			ExpectedTargetRevision:   staleSparseTarget.Msg.TargetRevision,
			ContributorMemberIds:     []string{memberID},
			LocaleValues: emailLayoutLocaleValueMessages(map[string]string{
				sourceUnits[0].Handle: "Bearbeiteter Titel",
			}),
		},
	))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err),
		"source edit invalidates an out-of-date target room")
	require.Len(t, auditAppender.records, 2)

	reloadedSparseTarget, err := service.LoadDocument(ctx, connect.NewRequest(
		&intrav1.LoadEmailLayoutDocumentRequest{EmailLayoutId: layoutID, Locale: "de"},
	))
	require.NoError(t, err)
	require.Equal(t, sparseValues, emailLayoutLocaleValueMap(reloadedSparseTarget.Msg.LocaleValues))
	require.Equal(t, "Changed source body", reloadedSparseTarget.Msg.Units[1].SourceValue)
	require.NotEqual(t, staleSparseTarget.Msg.TargetRevision, reloadedSparseTarget.Msg.TargetRevision)
	storedSparseTarget, err := emailutil.LoadLayoutTranslationEntry(ctx, db, layoutID, "de")
	require.NoError(t, err)
	require.True(t, sparseUpdatedAt.Equal(storedSparseTarget.UpdatedAt))
	storedSparseValues, err := emailutil.ExtractLayoutStoredLocaleValues(*storedSparseTarget.ContentHTML)
	require.NoError(t, err)
	require.Equal(t, sparseValues, storedSparseValues)

	storedTarget, err = emailutil.LoadLayoutTranslationEntry(ctx, db, layoutID, "ko")
	require.NoError(t, err)
	require.True(t, targetUpdatedAt.Equal(storedTarget.UpdatedAt),
		"source representation normalization must preserve target write facts")
	storedValues, err = emailutil.ExtractLayoutStoredLocaleValues(*storedTarget.ContentHTML)
	require.NoError(t, err)
	require.Equal(t, nextValues, storedValues)

	noOpSource, err := service.SaveDocument(ctx, connect.NewRequest(
		&intrav1.SaveEmailLayoutDocumentRequest{
			EmailLayoutId: layoutID, Locale: "en",
			ExpectedDocumentRevision: savedSource.Msg.DocumentRevision,
			ContributorMemberIds:     []string{memberID},
			ContentHtml:              changedSourceHTML,
		},
	))
	require.NoError(t, err)
	require.Equal(t, savedSource.Msg.DocumentRevision, noOpSource.Msg.DocumentRevision)
	require.Len(t, auditAppender.records, 2, "semantic source no-op must not append Audit")

	var legacySourceStateTable sql.NullString
	require.NoError(t, db.Raw(`SELECT to_regclass('public.translation_source_state')::text`).Scan(&legacySourceStateTable).Error)
	require.False(t, legacySourceStateTable.Valid)
}

func emailLayoutLocaleValueMessages(values map[string]string) []*intrav1.EmailLayoutLocaleValue {
	result := make([]*intrav1.EmailLayoutLocaleValue, 0, len(values))
	for handle, value := range values {
		result = append(result, &intrav1.EmailLayoutLocaleValue{Handle: handle, Value: value})
	}
	return result
}

func emailLayoutLocaleValueMap(values []*intrav1.EmailLayoutLocaleValue) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		result[value.Handle] = value.Value
	}
	return result
}

func requireEmailLayoutLocaleAudit(
	t *testing.T,
	record sharedtelemetry.AuditRecord,
	memberID string,
	locale string,
) {
	t.Helper()
	require.Equal(t, sharedtelemetry.AuditEmailLayoutUpdated, record.Action)
	require.Equal(t, []string{"locale_content"}, record.ChangedFields)
	require.Equal(t, locale, record.Locale)
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, record.ItemOperation)
	require.Equal(t, memberID, record.MemberID)
	require.Empty(t, record.ContributorMemberIDs)
}
