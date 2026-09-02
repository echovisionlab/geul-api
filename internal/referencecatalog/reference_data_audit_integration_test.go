//go:build integration

package referencecatalog

import (
	"encoding/json"
	"sort"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

type referenceDataAuditRecord struct {
	Action        string
	TargetType    string `gorm:"column:target_type"`
	TargetID      string `gorm:"column:target_id"`
	ActorMemberID string `gorm:"column:actor_member_id"`
	RequestID     string `gorm:"column:request_id"`
	Attributes    []byte `gorm:"column:attributes"`
}

func TestReferenceDataAuditsExactCRUDAndSkipsNoOpsIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	ctx = referenceAuditedRequestContext(t, ctx)
	memberID := auth.GetUser(ctx).MemberID.String()
	writer := apitelemetry.NewDurableWriter(db)

	category := NewAuditedCategoryService(db, writer, referenceMenuTargetsForTest(writer), spiceDB)
	categorySlug := "reference-audit-category"
	categoryDescription := "audited category"
	createdCategory, err := category.CreateCategory(ctx, connect.NewRequest(&managev1.CreateCategoryRequest{Name: "Reference audit category", Slug: &categorySlug, Description: &categoryDescription}))
	require.NoError(t, err)
	categoryName := "Reference audit category updated"
	updatedCategorySlug := "reference-audit-category-updated"
	updatedCategoryDescription := "updated category"
	_, err = category.UpdateCategory(ctx, connect.NewRequest(&managev1.UpdateCategoryRequest{Id: createdCategory.Msg.Id, Name: &categoryName, Slug: &updatedCategorySlug, Description: &updatedCategoryDescription}))
	require.NoError(t, err)
	_, err = category.UpdateCategory(ctx, connect.NewRequest(&managev1.UpdateCategoryRequest{Id: createdCategory.Msg.Id, Name: &categoryName, Slug: &updatedCategorySlug, Description: &updatedCategoryDescription}))
	require.NoError(t, err)
	_, err = category.DeleteCategory(ctx, connect.NewRequest(&managev1.DeleteCategoryRequest{Id: createdCategory.Msg.Id}))
	require.NoError(t, err)

	tag := NewAuditedTagService(db, writer, referenceMenuTargetsForTest(writer), spiceDB)
	tagSlug := "reference-audit-tag"
	createdTag, err := tag.CreateTag(ctx, connect.NewRequest(&managev1.CreateTagRequest{Name: "Reference audit tag", Slug: &tagSlug}))
	require.NoError(t, err)
	tagName := "Reference audit tag updated"
	updatedTagSlug := "reference-audit-tag-updated"
	_, err = tag.UpdateTag(ctx, connect.NewRequest(&managev1.UpdateTagRequest{Id: createdTag.Msg.Id, Name: &tagName, Slug: &updatedTagSlug}))
	require.NoError(t, err)
	_, err = tag.UpdateTag(ctx, connect.NewRequest(&managev1.UpdateTagRequest{Id: createdTag.Msg.Id, Name: &tagName, Slug: &updatedTagSlug}))
	require.NoError(t, err)
	_, err = tag.DeleteTag(ctx, connect.NewRequest(&managev1.DeleteTagRequest{Id: createdTag.Msg.Id}))
	require.NoError(t, err)

	var records []referenceDataAuditRecord
	require.NoError(t, db.Raw(`
		SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id,
		       request_id::text AS request_id, attributes
		FROM public.domain_audit
		ORDER BY action, target_id
	`).Scan(&records).Error)
	require.Len(t, records, 6)

	assertReferenceDataAuditRecords(t, records, memberID, sharedtelemetry.RequestIDFromContext(ctx), map[string]referenceDataAuditExpectation{
		string(sharedtelemetry.AuditCategoryCreated): {targetType: "category", targetID: createdCategory.Msg.Id},
		string(sharedtelemetry.AuditCategoryUpdated): {targetType: "category", targetID: createdCategory.Msg.Id, changedFields: []string{"description", "name", "slug"}},
		string(sharedtelemetry.AuditCategoryDeleted): {targetType: "category", targetID: createdCategory.Msg.Id},
		string(sharedtelemetry.AuditTagCreated):      {targetType: "tag", targetID: createdTag.Msg.Id},
		string(sharedtelemetry.AuditTagUpdated):      {targetType: "tag", targetID: createdTag.Msg.Id, changedFields: []string{"name", "slug"}},
		string(sharedtelemetry.AuditTagDeleted):      {targetType: "tag", targetID: createdTag.Msg.Id},
	})
	_, err = category.CreateCategory(ctx, connect.NewRequest(&managev1.CreateCategoryRequest{Name: ""}))
	require.Error(t, err)
	var auditCount int64
	require.NoError(t, db.Table("public.domain_audit").Count(&auditCount).Error)
	require.EqualValues(t, 6, auditCount)
}

func TestReferenceDataAuditAppendFailureRollsBackCreateIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	name := "Reference audit rollback"
	slug := "reference-audit-rollback"
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	ctx = referenceAuditedRequestContext(t, ctx)
	_, err := NewAuditedCategoryService(db, referenceFailingAuditAppender{}, referenceMenuTargetsForTest(referenceFailingAuditAppender{}), spiceDB).CreateCategory(
		ctx,
		connect.NewRequest(&managev1.CreateCategoryRequest{Name: name, Slug: &slug}),
	)
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Table("public.category").Where("slug = ?", slug).Count(&count).Error)
	require.Zero(t, count)

}

func TestReferenceDataMenuTargetRewritesAuditAndRollBackTogetherIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	ctx = referenceAuditedRequestContext(t, ctx)
	memberID := auth.GetUser(ctx).MemberID.String()

	categorySlug := "reference-menu-category"
	category, err := NewCategoryService(db, referenceMenuTargetsForTest(nil), spiceDB).CreateCategory(ctx, connect.NewRequest(&managev1.CreateCategoryRequest{
		Name: "Reference menu category", Slug: &categorySlug,
	}))
	require.NoError(t, err)
	tagSlug := "reference-menu-tag"
	tag, err := NewTagService(db, referenceMenuTargetsForTest(nil), spiceDB).CreateTag(ctx, connect.NewRequest(&managev1.CreateTagRequest{
		Name: "Reference menu tag", Slug: &tagSlug,
	}))
	require.NoError(t, err)
	categoryMenuID := seedReferenceMenuTarget(t, db, "Reference menu category source", model.MenuItem{
		ID: "category-target", Label: "Category", LinkType: "category",
		TargetID: &category.Msg.Id, TargetSlug: &categorySlug,
	})
	tagMenuID := seedReferenceMenuTarget(t, db, "Reference menu tag source", model.MenuItem{
		ID: "tag-target", Label: "Tag", LinkType: "tag",
		TargetID: &tag.Msg.Id, TargetSlug: &tagSlug,
	})

	writer := apitelemetry.NewDurableWriter(db)
	updatedCategorySlug := "reference-menu-category-updated"
	_, err = NewAuditedCategoryService(db, writer, referenceMenuTargetsForTest(writer), spiceDB).UpdateCategory(ctx, connect.NewRequest(&managev1.UpdateCategoryRequest{
		Id: category.Msg.Id, Slug: &updatedCategorySlug,
	}))
	require.NoError(t, err)
	_, err = NewAuditedTagService(db, writer, referenceMenuTargetsForTest(writer), spiceDB).DeleteTag(ctx, connect.NewRequest(&managev1.DeleteTagRequest{Id: tag.Msg.Id}))
	require.NoError(t, err)

	var records []referenceDataAuditRecord
	require.NoError(t, db.Raw(`
		SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id,
		       request_id::text AS request_id, attributes
		FROM public.domain_audit
		ORDER BY action, target_id
	`).Scan(&records).Error)
	require.Len(t, records, 4)
	var rootRecords []referenceDataAuditRecord
	for _, record := range records {
		if record.Action != string(sharedtelemetry.AuditMenuUpdated) {
			rootRecords = append(rootRecords, record)
		}
	}
	assertReferenceDataAuditRecords(t, rootRecords, memberID, sharedtelemetry.RequestIDFromContext(ctx), map[string]referenceDataAuditExpectation{
		string(sharedtelemetry.AuditCategoryUpdated): {targetType: "category", targetID: category.Msg.Id, changedFields: []string{"slug"}},
		string(sharedtelemetry.AuditTagDeleted):      {targetType: "tag", targetID: tag.Msg.Id},
	})
	// Each top-level request has its own correlation; assert both changed Menu
	// roots separately rather than collapsing two same-action rows by action.
	var menuRecords []referenceDataAuditRecord
	require.NoError(t, db.Raw(`
		SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id,
		       request_id::text AS request_id, attributes
		FROM public.domain_audit
		WHERE action = ?
		ORDER BY target_id
	`, sharedtelemetry.AuditMenuUpdated).Scan(&menuRecords).Error)
	require.Len(t, menuRecords, 2)
	expectedMenuIDs := []string{categoryMenuID, tagMenuID}
	sort.Strings(expectedMenuIDs)
	require.Equal(t, expectedMenuIDs, []string{menuRecords[0].TargetID, menuRecords[1].TargetID})
	for _, record := range menuRecords {
		require.Equal(t, memberID, record.ActorMemberID)
		require.JSONEq(t, `{"changed_fields":["items"]}`, string(record.Attributes))
	}

	var categoryItems string
	require.NoError(t, db.Table("public.menu").Select("items::text").Where("id = ?", categoryMenuID).Scan(&categoryItems).Error)
	require.Contains(t, categoryItems, updatedCategorySlug)
	var tagItems string
	require.NoError(t, db.Table("public.menu").Select("items::text").Where("id = ?", tagMenuID).Scan(&tagItems).Error)
	require.NotContains(t, tagItems, tag.Msg.Id)

	failingSlug := "reference-menu-failing"
	failingCategory, err := NewCategoryService(db, referenceMenuTargetsForTest(nil), spiceDB).CreateCategory(ctx, connect.NewRequest(&managev1.CreateCategoryRequest{
		Name: "Reference menu failing category", Slug: &failingSlug,
	}))
	require.NoError(t, err)
	failingMenuID := seedReferenceMenuTarget(t, db, "Reference menu failing source", model.MenuItem{
		ID: "failing-category-target", Label: "Failing category", LinkType: "category",
		TargetID: &failingCategory.Msg.Id, TargetSlug: &failingSlug,
	})
	rolledBackSlug := "reference-menu-failing-updated"
	_, err = NewAuditedCategoryService(db, referenceFailingAuditAppender{}, referenceMenuTargetsForTest(referenceFailingAuditAppender{}), spiceDB).UpdateCategory(ctx, connect.NewRequest(&managev1.UpdateCategoryRequest{
		Id: failingCategory.Msg.Id, Slug: &rolledBackSlug,
	}))
	require.Error(t, err)
	var storedSlug string
	require.NoError(t, db.Table("public.category").Select("slug").Where("id = ?", failingCategory.Msg.Id).Scan(&storedSlug).Error)
	require.Equal(t, failingSlug, storedSlug)
	var failingMenuItems string
	require.NoError(t, db.Table("public.menu").Select("items::text").Where("id = ?", failingMenuID).Scan(&failingMenuItems).Error)
	require.Contains(t, failingMenuItems, failingSlug)
	var auditCount int64
	require.NoError(t, db.Table("public.domain_audit").Count(&auditCount).Error)
	require.EqualValues(t, 4, auditCount)
}

type referenceDataAuditExpectation struct {
	targetType    string
	targetID      string
	changedFields []string
}

func assertReferenceDataAuditRecords(
	t *testing.T,
	records []referenceDataAuditRecord,
	memberID string,
	requestID string,
	expected map[string]referenceDataAuditExpectation,
) {
	t.Helper()
	for _, record := range records {
		expectation, ok := expected[record.Action]
		require.True(t, ok, record.Action)
		require.Equal(t, memberID, record.ActorMemberID)
		require.Equal(t, requestID, record.RequestID)
		require.Equal(t, expectation.targetType, record.TargetType)
		require.Equal(t, expectation.targetID, record.TargetID)
		expectedAttributes := `{}`
		if len(expectation.changedFields) > 0 {
			attributes, err := json.Marshal(struct {
				ChangedFields []string `json:"changed_fields"`
			}{ChangedFields: expectation.changedFields})
			require.NoError(t, err)
			expectedAttributes = string(attributes)
		}
		require.JSONEq(t, expectedAttributes, string(record.Attributes), record.Action)
		delete(expected, record.Action)
	}
	require.Empty(t, expected)
}
