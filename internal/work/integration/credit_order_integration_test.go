//go:build integration

package integration

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestWorkCreditMoveIntentPersistsCanonicalOrderIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Work Credit Order Admin")
	ctx := workIntegrationAdminCtx(adminID)
	service := newWorkIntegrationService(t, db, adminID, referenceNoopFileDeleter{})
	workID := createCreditOrderWork(t, service, ctx, "Credit Order Main")

	groupOne := createCreditOrderGroup(t, service, ctx, workID, "Group One")
	creditOne := createCreditOrderCredit(t, service, ctx, workID, &groupOne, "Credit One")
	creditTwo := createCreditOrderCredit(t, service, ctx, workID, &groupOne, "Credit Two")
	ungrouped := createCreditOrderCredit(t, service, ctx, workID, nil, "Ungrouped")
	groupTwo := createCreditOrderGroup(t, service, ctx, workID, "Group Two")
	groupTwoCredit := createCreditOrderCredit(t, service, ctx, workID, &groupTwo, "Group Two Credit")

	movedGroup, err := service.MoveWorkCreditItem(ctx, connect.NewRequest(&managev1.MoveWorkCreditItemRequest{
		WorkId: workID,
		Kind:   managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP,
		ItemId: groupTwo,
		Before: creditOrderAnchor(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, ungrouped),
	}))
	require.NoError(t, err)
	require.True(t, movedGroup.Msg.Changed)
	require.Equal(t, []string{"g:" + groupOne, "c:" + creditOne, "c:" + creditTwo, "g:" + groupTwo, "c:" + groupTwoCredit, "c:" + ungrouped}, creditOrderKeys(movedGroup.Msg.Items))

	movedCredit, err := service.MoveWorkCreditItem(ctx, connect.NewRequest(&managev1.MoveWorkCreditItemRequest{
		WorkId:        workID,
		Kind:          managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT,
		ItemId:        creditOne,
		TargetGroupId: ptrString(groupOne),
		After:         creditOrderAnchor(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, creditTwo),
		Before:        creditOrderAnchor(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, groupTwoCredit),
	}))
	require.NoError(t, err)
	require.True(t, movedCredit.Msg.Changed)
	wantOrder := []string{"g:" + groupOne, "c:" + creditTwo, "c:" + creditOne, "g:" + groupTwo, "c:" + groupTwoCredit, "c:" + ungrouped}
	require.Equal(t, wantOrder, creditOrderKeys(movedCredit.Msg.Items))

	reloaded, err := service.GetWorkCredits(ctx, connect.NewRequest(&managev1.GetWorkCreditsRequest{WorkId: workID}))
	require.NoError(t, err)
	require.Equal(t, wantOrder, creditOrderKeys(reloaded.Msg.Order))
	requireCanonicalCreditSortIndices(t, db, workID, wantOrder)

	transitionCredit := createCreditOrderCredit(t, service, ctx, workID, nil, "Group Change Credit")
	_, err = service.UpdateWorkCredit(ctx, connect.NewRequest(&managev1.UpdateWorkCreditRequest{
		CreditId: transitionCredit,
		GroupId:  ptrString(groupOne),
	}))
	require.NoError(t, err)
	groupChanged, err := service.GetWorkCredits(ctx, connect.NewRequest(&managev1.GetWorkCreditsRequest{WorkId: workID}))
	require.NoError(t, err)
	require.Equal(t,
		[]string{"g:" + groupOne, "c:" + creditTwo, "c:" + creditOne, "c:" + transitionCredit, "g:" + groupTwo, "c:" + groupTwoCredit, "c:" + ungrouped},
		creditOrderKeys(groupChanged.Msg.Order),
	)
	_, err = service.DeleteWorkCredit(ctx, connect.NewRequest(&managev1.DeleteWorkCreditRequest{CreditId: transitionCredit}))
	require.NoError(t, err)
	deletedCredit, err := service.GetWorkCredits(ctx, connect.NewRequest(&managev1.GetWorkCreditsRequest{WorkId: workID}))
	require.NoError(t, err)
	require.Equal(t, wantOrder, creditOrderKeys(deletedCredit.Msg.Order))
	requireCanonicalCreditSortIndices(t, db, workID, wantOrder)

	foreignWorkID := createCreditOrderWork(t, service, ctx, "Credit Order Foreign")
	foreignGroup := createCreditOrderGroup(t, service, ctx, foreignWorkID, "Foreign Group")
	foreignCredit := createCreditOrderCredit(t, service, ctx, foreignWorkID, &foreignGroup, "Foreign Credit")
	_, err = service.MoveWorkCreditItem(ctx, connect.NewRequest(&managev1.MoveWorkCreditItemRequest{
		WorkId:        workID,
		Kind:          managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT,
		ItemId:        creditOne,
		TargetGroupId: ptrString(foreignGroup),
		Before:        creditOrderAnchor(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, foreignCredit),
	}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = service.MoveWorkCreditItem(ctx, connect.NewRequest(&managev1.MoveWorkCreditItemRequest{
		WorkId:        workID,
		Kind:          managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT,
		ItemId:        foreignCredit,
		TargetGroupId: ptrString(groupOne),
	}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = service.MoveWorkCreditItem(ctx, connect.NewRequest(&managev1.MoveWorkCreditItemRequest{
		WorkId:        workID,
		Kind:          managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT,
		ItemId:        creditOne,
		TargetGroupId: ptrString(groupOne),
		Before:        creditOrderAnchor(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, foreignCredit),
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = service.MoveWorkCreditItem(ctx, connect.NewRequest(&managev1.MoveWorkCreditItemRequest{
		WorkId:        workID,
		Kind:          managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT,
		ItemId:        creditOne,
		TargetGroupId: ptrString(groupOne),
		Before:        creditOrderAnchor(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, foreignGroup),
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = service.MoveWorkCreditItem(ctx, connect.NewRequest(&managev1.MoveWorkCreditItemRequest{
		WorkId:        workID,
		Kind:          managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT,
		ItemId:        creditOne,
		TargetGroupId: ptrString(groupOne),
		Before:        creditOrderAnchor(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, groupTwoCredit),
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = service.MoveWorkCreditItem(ctx, connect.NewRequest(&managev1.MoveWorkCreditItemRequest{
		WorkId: workID,
		Kind:   managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP,
		ItemId: groupOne,
		After:  creditOrderAnchor(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP, foreignGroup),
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = service.MoveWorkCreditItem(ctx, connect.NewRequest(&managev1.MoveWorkCreditItemRequest{
		WorkId: workID,
		Kind:   managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP,
		ItemId: groupOne,
		After:  creditOrderAnchor(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, foreignCredit),
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = service.DeleteWorkCreditGroup(ctx, connect.NewRequest(&managev1.DeleteWorkCreditGroupRequest{GroupId: groupTwo}))
	require.NoError(t, err)
	deletedGroupOrder, err := service.GetWorkCredits(ctx, connect.NewRequest(&managev1.GetWorkCreditsRequest{WorkId: workID}))
	require.NoError(t, err)
	wantAfterDelete := []string{"g:" + groupOne, "c:" + creditTwo, "c:" + creditOne, "c:" + groupTwoCredit, "c:" + ungrouped}
	require.Equal(t, wantAfterDelete, creditOrderKeys(deletedGroupOrder.Msg.Order))
	requireCanonicalCreditSortIndices(t, db, workID, wantAfterDelete)
	_, err = service.MoveWorkCreditItem(ctx, connect.NewRequest(&managev1.MoveWorkCreditItemRequest{
		WorkId:        workID,
		Kind:          managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT,
		ItemId:        creditOne,
		TargetGroupId: ptrString(groupTwo),
	}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	unauthorizedCtx := workIntegrationAdminCtx(integrationTestUUID())
	_, err = service.MoveWorkCreditItem(unauthorizedCtx, connect.NewRequest(&managev1.MoveWorkCreditItemRequest{
		WorkId:        workID,
		Kind:          managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT,
		ItemId:        creditOne,
		TargetGroupId: ptrString(groupOne),
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	deletedAnchorFallback, err := service.MoveWorkCreditItem(ctx, connect.NewRequest(&managev1.MoveWorkCreditItemRequest{
		WorkId:        workID,
		Kind:          managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT,
		ItemId:        creditTwo,
		TargetGroupId: ptrString(groupOne),
		After:         creditOrderAnchor(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, creditOne),
		Before:        creditOrderAnchor(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, integrationTestUUID()),
	}))
	require.NoError(t, err)
	require.Equal(t,
		[]string{"g:" + groupOne, "c:" + creditOne, "c:" + creditTwo, "c:" + groupTwoCredit, "c:" + ungrouped},
		creditOrderKeys(deletedAnchorFallback.Msg.Items),
	)
}

func TestUpdateWorkCreditGroupPreservesRequestEqualToStaleNameIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Work Credit Group Update Admin")
	ctx, cancel := context.WithTimeout(workIntegrationAdminCtx(adminID), 10*time.Second)
	defer cancel()
	service := newWorkIntegrationService(t, db, adminID, referenceNoopFileDeleter{})
	workID := createCreditOrderWork(t, service, ctx, "Credit Group Update")
	initialName := "Initial Group Name"
	groupID := createCreditOrderGroup(t, service, ctx, workID, initialName)
	concurrentName := "Concurrent Group Name"

	blocker := db.WithContext(ctx).Begin()
	require.NoError(t, blocker.Error)
	lockHeld := true
	t.Cleanup(func() {
		if lockHeld {
			_ = blocker.Rollback().Error
		}
	})
	require.NoError(t, blocker.Exec("SELECT id FROM work WHERE id = ? FOR UPDATE", workID).Error)

	groupRead := make(chan struct{})
	var groupReadOnce sync.Once
	callbackName := "test:signal_work_credit_group_stale_read"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "work_credit_group" {
			groupReadOnce.Do(func() { close(groupRead) })
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	updateResult := make(chan struct {
		response *connect.Response[managev1.WorkCreditGroup]
		err      error
	}, 1)
	go func() {
		response, err := service.UpdateWorkCreditGroup(ctx, connect.NewRequest(&managev1.UpdateWorkCreditGroupRequest{
			GroupId: groupID,
			Name:    &initialName,
		}))
		updateResult <- struct {
			response *connect.Response[managev1.WorkCreditGroup]
			err      error
		}{response: response, err: err}
	}()

	select {
	case <-groupRead:
	case <-ctx.Done():
		require.FailNow(t, "group update did not perform its initial read")
	}
	require.NoError(t, blocker.Model(&model.WorkCreditGroup{}).
		Where("id = ? AND work_id = ?", groupID, workID).
		Update("name", concurrentName).Error)
	require.NoError(t, blocker.Commit().Error)
	lockHeld = false

	select {
	case result := <-updateResult:
		require.NoError(t, result.err)
		require.NotNil(t, result.response)
		require.Equal(t, initialName, result.response.Msg.Name)
	case <-ctx.Done():
		require.FailNow(t, "group update did not resume after the Work lock was released")
	}

	var persisted model.WorkCreditGroup
	require.NoError(t, db.First(&persisted, "id = ? AND work_id = ?", groupID, workID).Error)
	require.Equal(t, initialName, persisted.Name)
}

func createCreditOrderWork(t *testing.T, service *workdomain.WorkService, ctx context.Context, title string) string {
	t.Helper()
	isPresent := true
	created, err := service.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title:     title + " " + integrationTestUUID(),
		Type:      managev1.WorkType_WORK_TYPE_ARTICLE,
		Year:      2026,
		Month:     10,
		IsPresent: &isPresent,
		Document:  emptyWorkIntegrationDocument("en"),
	}))
	require.NoError(t, err)
	return created.Msg.Id
}

func createCreditOrderGroup(t *testing.T, service *workdomain.WorkService, ctx context.Context, workID, name string) string {
	t.Helper()
	created, err := service.CreateWorkCreditGroup(ctx, connect.NewRequest(&managev1.CreateWorkCreditGroupRequest{
		WorkId: workID,
		Name:   name,
	}))
	require.NoError(t, err)
	return created.Msg.Id
}

func createCreditOrderCredit(t *testing.T, service *workdomain.WorkService, ctx context.Context, workID string, groupID *string, name string) string {
	t.Helper()
	created, err := service.AddWorkCredit(ctx, connect.NewRequest(&managev1.AddWorkCreditRequest{
		WorkId:  workID,
		GroupId: groupID,
		Name:    &name,
	}))
	require.NoError(t, err)
	return created.Msg.Id
}

func creditOrderAnchor(kind managev1.WorkCreditItemKind, id string) *managev1.WorkCreditOrderItem {
	return &managev1.WorkCreditOrderItem{Kind: kind, Id: id}
}

func creditOrderKeys(items []*managev1.WorkCreditOrderItem) []string {
	result := make([]string, len(items))
	for index, item := range items {
		prefix := "c:"
		if item.Kind == managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP {
			prefix = "g:"
		}
		result[index] = prefix + item.Id
	}
	return result
}

func requireCanonicalCreditSortIndices(t *testing.T, db *gorm.DB, workID string, expected []string) {
	t.Helper()
	var groups []model.WorkCreditGroup
	var credits []model.WorkCredit
	require.NoError(t, db.Where("work_id = ?", workID).Find(&groups).Error)
	require.NoError(t, db.Where("work_id = ?", workID).Find(&credits).Error)
	type orderedRow struct {
		key       string
		sortOrder int
	}
	rows := make([]orderedRow, 0, len(groups)+len(credits))
	for _, group := range groups {
		rows = append(rows, orderedRow{key: "g:" + group.ID, sortOrder: group.SortOrder})
	}
	for _, credit := range credits {
		rows = append(rows, orderedRow{key: "c:" + credit.ID, sortOrder: credit.SortOrder})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].sortOrder < rows[j].sortOrder })
	require.Len(t, rows, len(expected))
	keys := make([]string, len(rows))
	for index, row := range rows {
		require.Equal(t, index+1, row.sortOrder, "sort_order must be one global contiguous index")
		keys[index] = row.key
	}
	require.Equal(t, expected, keys)
}
