package work

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestBuildWorkCreditOrderUsesUniqueFlattenedIndices(t *testing.T) {
	groups := []model.WorkCreditGroup{{ID: "g1", SortOrder: 1}, {ID: "g2", SortOrder: 4}}
	credits := []model.WorkCredit{
		{ID: "c1", GroupID: testStringPointer("g1"), SortOrder: 2},
		{ID: "u1", SortOrder: 3},
		{ID: "c2", GroupID: testStringPointer("g2"), SortOrder: 5},
	}

	order, valid := buildWorkCreditOrder(groups, credits)

	require.True(t, valid)
	require.Equal(t, []string{"g:g1", "c:c1", "c:u1", "g:g2", "c:c2"}, orderKeys(order))
}

func TestBuildWorkCreditOrderFallsBackForLegacyDuplicates(t *testing.T) {
	groups := []model.WorkCreditGroup{{ID: "g2", SortOrder: 0}, {ID: "g1", SortOrder: 0}}
	credits := []model.WorkCredit{
		{ID: "c2", GroupID: testStringPointer("g1"), SortOrder: 0},
		{ID: "c1", GroupID: testStringPointer("g1"), SortOrder: 0},
		{ID: "u1", SortOrder: 0},
	}

	order, valid := buildWorkCreditOrder(groups, credits)

	require.False(t, valid)
	require.Equal(t, []string{"g:g1", "c:c1", "c:c2", "g:g2", "c:u1"}, orderKeys(order))
}

func TestMoveWorkCreditGroupOrderKeepsSectionTogetherAtMixedAnchor(t *testing.T) {
	order := testCreditOrder()

	result, changed := moveWorkCreditGroupOrder(order, "g1", orderItem(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, "u1"), nil)

	require.True(t, changed)
	require.Equal(t, []string{"c:u1", "g:g1", "c:c1", "c:c2", "g:g2", "c:c3", "c:u2"}, orderKeys(result))
}

func TestMoveWorkCreditGroupOrderNormalizesChildAnchorToSectionBoundary(t *testing.T) {
	order := testCreditOrder()

	result, changed := moveWorkCreditGroupOrder(order, "g1", orderItem(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, "c3"), nil)

	require.True(t, changed)
	require.Equal(t, []string{"c:u1", "g:g2", "c:c3", "g:g1", "c:c1", "c:c2", "c:u2"}, orderKeys(result))
}

func TestMoveWorkCreditGroupOrderTreatsOwnChildAnchorAsSameSection(t *testing.T) {
	order := testCreditOrder()

	result, changed := moveWorkCreditGroupOrder(
		order,
		"g1",
		orderItem(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, "c1"),
		orderItem(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, "c2"),
	)

	require.False(t, changed)
	require.Equal(t, orderKeys(order), orderKeys(result))
}

func TestMoveWorkCreditGroupOrderPrefersBeforeWhenAnchorsReverse(t *testing.T) {
	order := testCreditOrder()

	result, changed := moveWorkCreditGroupOrder(
		order,
		"g1",
		orderItem(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, "c3"),
		orderItem(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, "u1"),
	)

	require.False(t, changed)
	require.Equal(t, orderKeys(order), orderKeys(result))
}

func TestMoveWorkCreditOrderUsesBeforeThenAfterThenGroupEnd(t *testing.T) {
	order := testCreditOrder()
	g1 := "g1"

	beforeWinsWhenAnchorsReverse, changed := moveWorkCreditOrder(
		order,
		"c3",
		&g1,
		orderItem(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, "c2"),
		orderItem(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, "c1"),
	)
	require.True(t, changed)
	require.Equal(t, []string{"g:g1", "c:c3", "c:c1", "c:c2", "c:u1", "g:g2", "c:u2"}, orderKeys(beforeWinsWhenAnchorsReverse))

	beforeWins, changed := moveWorkCreditOrder(
		order,
		"c3",
		&g1,
		orderItem(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, "c1"),
		orderItem(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, "c2"),
	)
	require.True(t, changed)
	require.Equal(t, []string{"g:g1", "c:c1", "c:c3", "c:c2", "c:u1", "g:g2", "c:u2"}, orderKeys(beforeWins))

	afterFallback, _ := moveWorkCreditOrder(
		order,
		"c3",
		&g1,
		orderItem(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, "c1"),
		orderItem(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, "deleted"),
	)
	require.Equal(t, []string{"g:g1", "c:c1", "c:c3", "c:c2", "c:u1", "g:g2", "c:u2"}, orderKeys(afterFallback))

	groupEnd, _ := moveWorkCreditOrder(order, "c3", &g1, nil, orderItem(managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, "u1"))
	require.Equal(t, []string{"g:g1", "c:c1", "c:c2", "c:c3", "c:u1", "g:g2", "c:u2"}, orderKeys(groupEnd))
}

func TestMoveWorkCreditOrderUngroupedWithoutAnchorsAppendsToFlatEnd(t *testing.T) {
	order := testCreditOrder()
	result, changed := moveWorkCreditOrder(order, "c1", nil, nil, nil)

	require.True(t, changed)
	require.Equal(t, []string{"g:g1", "c:c2", "c:u1", "g:g2", "c:c3", "c:u2", "c:c1"}, orderKeys(result))
	last := result[len(result)-1]
	require.Nil(t, last.groupID)
}

func testCreditOrder() []workCreditOrderEntry {
	return []workCreditOrderEntry{
		{kind: managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP, id: "g1"},
		{kind: managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, id: "c1", groupID: testStringPointer("g1")},
		{kind: managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, id: "c2", groupID: testStringPointer("g1")},
		{kind: managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, id: "u1"},
		{kind: managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP, id: "g2"},
		{kind: managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, id: "c3", groupID: testStringPointer("g2")},
		{kind: managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_CREDIT, id: "u2"},
	}
}

func orderItem(kind managev1.WorkCreditItemKind, id string) *managev1.WorkCreditOrderItem {
	return &managev1.WorkCreditOrderItem{Kind: kind, Id: id}
}

func orderKeys(order []workCreditOrderEntry) []string {
	keys := make([]string, len(order))
	for index, entry := range order {
		prefix := "c:"
		if entry.kind == managev1.WorkCreditItemKind_WORK_CREDIT_ITEM_KIND_GROUP {
			prefix = "g:"
		}
		keys[index] = prefix + entry.id
	}
	return keys
}

func testStringPointer(value string) *string {
	return &value
}
