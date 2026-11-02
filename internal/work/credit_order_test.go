package work

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildWorkCreditOrderPreservesCanonicalMixedOrder(t *testing.T) {
	groups := []CreditGroup{{ID: "g1", SortOrder: 1}, {ID: "g2", SortOrder: 4}}
	credits := []Credit{
		{ID: "c1", GroupID: testStringPointer("g1"), SortOrder: 2},
		{ID: "u1", SortOrder: 3},
		{ID: "c2", GroupID: testStringPointer("g2"), SortOrder: 5},
	}

	order, err := buildWorkCreditOrder(groups, credits)

	require.NoError(t, err)
	require.Equal(t, []string{"g:g1", "c:c1", "c:u1", "g:g2", "c:c2"}, orderKeys(order))
}

func TestBuildWorkCreditOrderRejectsInvalidPersistedTopology(t *testing.T) {
	tests := []struct {
		name    string
		groups  []CreditGroup
		credits []Credit
	}{
		{
			name: "duplicate indices",
			groups: []CreditGroup{
				{ID: "g1", SortOrder: 1},
				{ID: "g2", SortOrder: 1},
			},
		},
		{
			name: "orphan group reference",
			credits: []Credit{
				{ID: "c1", GroupID: testStringPointer("missing"), SortOrder: 1},
			},
		},
		{
			name:   "noncontiguous group section",
			groups: []CreditGroup{{ID: "g1", SortOrder: 1}},
			credits: []Credit{
				{ID: "c1", GroupID: testStringPointer("g1"), SortOrder: 2},
				{ID: "u1", SortOrder: 3},
				{ID: "c2", GroupID: testStringPointer("g1"), SortOrder: 4},
			},
		},
		{
			name:   "noncontiguous global indices",
			groups: []CreditGroup{{ID: "g1", SortOrder: 1}},
			credits: []Credit{
				{ID: "u1", SortOrder: 3},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			order, err := buildWorkCreditOrder(test.groups, test.credits)

			require.Error(t, err)
			require.Nil(t, order)
		})
	}
}

func TestMoveWorkCreditGroupOrderKeepsSectionTogetherAtMixedAnchor(t *testing.T) {
	order := testCreditOrder()

	result, changed := moveWorkCreditGroupOrder(order, "g1", orderItem(CreditItemCredit, "u1"), nil)

	require.True(t, changed)
	require.Equal(t, []string{"c:u1", "g:g1", "c:c1", "c:c2", "g:g2", "c:c3", "c:u2"}, orderKeys(result))
}

func TestMoveWorkCreditGroupOrderNormalizesChildAnchorToSectionBoundary(t *testing.T) {
	order := testCreditOrder()

	result, changed := moveWorkCreditGroupOrder(order, "g1", orderItem(CreditItemCredit, "c3"), nil)

	require.True(t, changed)
	require.Equal(t, []string{"c:u1", "g:g2", "c:c3", "g:g1", "c:c1", "c:c2", "c:u2"}, orderKeys(result))
}

func TestMoveWorkCreditGroupOrderTreatsOwnChildAnchorAsSameSection(t *testing.T) {
	order := testCreditOrder()

	result, changed := moveWorkCreditGroupOrder(
		order,
		"g1",
		orderItem(CreditItemCredit, "c1"),
		orderItem(CreditItemCredit, "c2"),
	)

	require.False(t, changed)
	require.Equal(t, orderKeys(order), orderKeys(result))
}

func TestMoveWorkCreditGroupOrderPrefersBeforeWhenAnchorsReverse(t *testing.T) {
	order := testCreditOrder()

	result, changed := moveWorkCreditGroupOrder(
		order,
		"g1",
		orderItem(CreditItemCredit, "c3"),
		orderItem(CreditItemCredit, "u1"),
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
		orderItem(CreditItemCredit, "c2"),
		orderItem(CreditItemCredit, "c1"),
	)
	require.True(t, changed)
	require.Equal(t, []string{"g:g1", "c:c3", "c:c1", "c:c2", "c:u1", "g:g2", "c:u2"}, orderKeys(beforeWinsWhenAnchorsReverse))

	beforeWins, changed := moveWorkCreditOrder(
		order,
		"c3",
		&g1,
		orderItem(CreditItemCredit, "c1"),
		orderItem(CreditItemCredit, "c2"),
	)
	require.True(t, changed)
	require.Equal(t, []string{"g:g1", "c:c1", "c:c3", "c:c2", "c:u1", "g:g2", "c:u2"}, orderKeys(beforeWins))

	afterFallback, _ := moveWorkCreditOrder(
		order,
		"c3",
		&g1,
		orderItem(CreditItemCredit, "c1"),
		orderItem(CreditItemCredit, "deleted"),
	)
	require.Equal(t, []string{"g:g1", "c:c1", "c:c3", "c:c2", "c:u1", "g:g2", "c:u2"}, orderKeys(afterFallback))

	groupEnd, _ := moveWorkCreditOrder(order, "c3", &g1, nil, orderItem(CreditItemCredit, "u1"))
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
		{kind: CreditItemGroup, id: "g1"},
		{kind: CreditItemCredit, id: "c1", groupID: testStringPointer("g1")},
		{kind: CreditItemCredit, id: "c2", groupID: testStringPointer("g1")},
		{kind: CreditItemCredit, id: "u1"},
		{kind: CreditItemGroup, id: "g2"},
		{kind: CreditItemCredit, id: "c3", groupID: testStringPointer("g2")},
		{kind: CreditItemCredit, id: "u2"},
	}
}

func orderItem(kind CreditItemKind, id string) *OrderItem {
	return &OrderItem{Kind: kind, ID: id}
}

func orderKeys(order []workCreditOrderEntry) []string {
	keys := make([]string, len(order))
	for index, entry := range order {
		prefix := "c:"
		if entry.kind == CreditItemGroup {
			prefix = "g:"
		}
		keys[index] = prefix + entry.id
	}
	return keys
}

func testStringPointer(value string) *string {
	return &value
}
