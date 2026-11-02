package work_test

import (
	"testing"

	"connectrpc.com/connect"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/mutation"
	optional "github.com/echovisionlab/geul-api/internal/optional"
	"github.com/echovisionlab/geul-api/internal/work"
	"github.com/echovisionlab/geul-api/internal/work/worktest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// orderKeys names an order as kind:id, a credit with its group.
func orderKeys(order []work.OrderItem) []string {
	keys := make([]string, 0, len(order))
	for _, item := range order {
		switch {
		case item.Kind == work.CreditItemGroup:
			keys = append(keys, "g:"+item.ID)
		case item.GroupID != nil:
			keys = append(keys, "c:"+item.ID+"@"+*item.GroupID)
		default:
			keys = append(keys, "c:"+item.ID)
		}
	}
	return keys
}

func (h *harness) credits(t *testing.T, workID string) work.Credits {
	t.Helper()
	credits, err := h.service.GetWorkCredits(newPrincipal().context(t), work.GetWorkCreditsQuery{WorkID: workID})
	require.NoError(t, err)
	return credits
}

func TestCreditsAppendAtTheirGroupEndAndKeepACanonicalOrder(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	ctx := admin.context(t)
	member := newPrincipal()
	h.store.PutMember(member.memberID, worktest.Member{Nickname: "Credited"})
	h.members.summaries[member.memberID] = &memberref.Summary{ID: member.memberID, Nickname: "Credited"}
	h.store.PutArtist(work.CreditArtistRow{ID: "artist-1", Name: "Artist", ImageFileID: stringPointer("image-1")})
	h.store.PutFileAsset("image-1", "image", &mediaasset.Reference{URL: "https://cdn.example/artist.webp"})
	value := h.seedWork(t, "Credited")

	group, err := h.service.CreateWorkCreditGroup(ctx, work.CreateWorkCreditGroupCommand{WorkID: value.ID, Name: "  Band  "})
	require.NoError(t, err)
	require.Equal(t, "Band", group.Name)
	ungrouped, err := h.service.AddWorkCredit(ctx, work.AddWorkCreditCommand{WorkID: value.ID, Name: stringPointer("Named"), CreditRole: stringPointer("Mixing")})
	require.NoError(t, err)
	grouped, err := h.service.AddWorkCredit(ctx, work.AddWorkCreditCommand{WorkID: value.ID, GroupID: &group.ID, MemberID: &member.memberID})
	require.NoError(t, err)
	require.Equal(t, "Credited", grouped.Member.Nickname)
	artist, err := h.service.AddWorkCredit(ctx, work.AddWorkCreditCommand{WorkID: value.ID, GroupID: &group.ID, ArtistID: stringPointer("artist-1")})
	require.NoError(t, err)
	require.Equal(t, "https://cdn.example/artist.webp", optional.Value(artist.Artist.ImageAsset).URL)

	credits := h.credits(t, value.ID)
	require.Equal(t, []string{"g:" + group.ID, "c:" + grouped.Credit.ID + "@" + group.ID, "c:" + artist.Credit.ID + "@" + group.ID, "c:" + ungrouped.Credit.ID}, orderKeys(credits.Order),
		"a grouped credit goes to its group's end, before the ungrouped one")
	require.Len(t, credits.Credits, 3)
	for index, credit := range h.store.StoredCredits(value.ID) {
		require.Equal(t, index+2, credit.SortOrder, "sort orders stay contiguous across groups and credits")
	}
	require.Len(t, h.audit.Records(), 4)
	for _, record := range h.audit.Records() {
		require.Equal(t, sharedtelemetry.AuditWorkUpdated, record.Action)
	}
}

func TestAddWorkCreditRejectsAGroupOfAnotherWorkAndAnIneligibleMember(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	value := h.seedWork(t, "Mine")
	other := h.seedWork(t, "Theirs")
	foreign := h.store.PutCreditGroup(work.CreditGroup{WorkID: other.ID, Name: "Foreign", SortOrder: 1})
	deleted := uuid.NewString()
	h.store.PutMember(deleted, worktest.Member{Nickname: "Gone", Deleted: true})

	_, err := h.service.AddWorkCredit(admin.context(t), work.AddWorkCreditCommand{WorkID: value.ID, GroupID: &foreign.ID, Name: stringPointer("Named")})
	requireCode(t, err, connect.CodeInvalidArgument, "group_id: must belong to this work")
	_, err = h.service.AddWorkCredit(admin.context(t), work.AddWorkCreditCommand{WorkID: value.ID, MemberID: &deleted})
	requireCode(t, err, connect.CodeInvalidArgument, "member_id: not found: "+deleted)
	require.Empty(t, h.store.StoredCredits(value.ID))
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
}

func TestMoveWorkCreditItemComposesWithTheLatestOrder(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	ctx := admin.context(t)
	value := h.seedWork(t, "Moves")
	group, err := h.service.CreateWorkCreditGroup(ctx, work.CreateWorkCreditGroupCommand{WorkID: value.ID, Name: "Group"})
	require.NoError(t, err)
	first, err := h.service.AddWorkCredit(ctx, work.AddWorkCreditCommand{WorkID: value.ID, Name: stringPointer("First")})
	require.NoError(t, err)
	second, err := h.service.AddWorkCredit(ctx, work.AddWorkCreditCommand{WorkID: value.ID, Name: stringPointer("Second")})
	require.NoError(t, err)

	moved, err := h.service.MoveWorkCreditItem(ctx, work.MoveWorkCreditItemCommand{
		WorkID: value.ID, Kind: work.CreditItemCredit, ItemID: second.Credit.ID, TargetGroupID: &group.ID,
	})
	require.NoError(t, err)
	require.True(t, moved.Changed)
	require.Equal(t, []string{"g:" + group.ID, "c:" + second.Credit.ID + "@" + group.ID, "c:" + first.Credit.ID}, orderKeys(moved.Items))
	require.Equal(t, moved.Items, h.credits(t, value.ID).Order)

	again, err := h.service.MoveWorkCreditItem(ctx, work.MoveWorkCreditItemCommand{
		WorkID: value.ID, Kind: work.CreditItemCredit, ItemID: second.Credit.ID, TargetGroupID: &group.ID,
	})
	require.NoError(t, err)
	require.False(t, again.Changed)
	records := len(h.audit.Records())

	_, err = h.service.MoveWorkCreditItem(ctx, work.MoveWorkCreditItemCommand{
		WorkID: value.ID, Kind: work.CreditItemGroup, ItemID: group.ID, After: &work.OrderItem{Kind: work.CreditItemCredit, ID: first.Credit.ID},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"c:" + first.Credit.ID, "g:" + group.ID, "c:" + second.Credit.ID + "@" + group.ID}, orderKeys(h.credits(t, value.ID).Order))
	require.Len(t, h.audit.Records(), records+1)
}

func TestMoveWorkCreditItemRejectsAnInvalidIntent(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	ctx := admin.context(t)
	value := h.seedWork(t, "Rejected")
	other := h.seedWork(t, "Other")
	credit, err := h.service.AddWorkCredit(ctx, work.AddWorkCreditCommand{WorkID: value.ID, Name: stringPointer("Credit")})
	require.NoError(t, err)
	foreign, err := h.service.AddWorkCredit(ctx, work.AddWorkCreditCommand{WorkID: other.ID, Name: stringPointer("Foreign")})
	require.NoError(t, err)
	missing := uuid.NewString()

	for name, test := range map[string]struct {
		command work.MoveWorkCreditItemCommand
		code    connect.Code
		message string
	}{
		"unknown kind": {work.MoveWorkCreditItemCommand{Kind: 7, ItemID: credit.Credit.ID},
			connect.CodeInvalidArgument, "kind: must identify a group or credit"},
		"credit without target": {work.MoveWorkCreditItemCommand{Kind: work.CreditItemCredit, ItemID: credit.Credit.ID},
			connect.CodeInvalidArgument, "target_group_id: is required for a credit move"},
		"self anchor": {work.MoveWorkCreditItemCommand{Kind: work.CreditItemCredit, ItemID: credit.Credit.ID, TargetGroupID: stringPointer(""),
			Before: &work.OrderItem{Kind: work.CreditItemCredit, ID: credit.Credit.ID}}, connect.CodeInvalidArgument, "anchor: cannot reference the moved item"},
		"missing credit": {work.MoveWorkCreditItemCommand{Kind: work.CreditItemCredit, ItemID: missing, TargetGroupID: stringPointer("")},
			connect.CodeNotFound, "credit not found: " + missing},
		"anchor of another work": {work.MoveWorkCreditItemCommand{Kind: work.CreditItemCredit, ItemID: credit.Credit.ID, TargetGroupID: stringPointer(""),
			After: &work.OrderItem{Kind: work.CreditItemCredit, ID: foreign.Credit.ID}}, connect.CodeInvalidArgument, "anchor: must reference a compatible item in this Work"},
		"malformed anchor": {work.MoveWorkCreditItemCommand{Kind: work.CreditItemCredit, ItemID: credit.Credit.ID, TargetGroupID: stringPointer(""),
			After: &work.OrderItem{Kind: work.CreditItemCredit, ID: "not-a-uuid"}}, connect.CodeInvalidArgument, "anchor.id: must be a valid item ID"},
	} {
		t.Run(name, func(t *testing.T) {
			command := test.command
			command.WorkID = value.ID
			_, err := h.service.MoveWorkCreditItem(ctx, command)
			requireCode(t, err, test.code, test.message)
		})
	}

	moved, err := h.service.MoveWorkCreditItem(ctx, work.MoveWorkCreditItemCommand{
		WorkID: value.ID, Kind: work.CreditItemCredit, ItemID: credit.Credit.ID, TargetGroupID: stringPointer(""),
		After: &work.OrderItem{Kind: work.CreditItemCredit, ID: missing},
	})
	require.NoError(t, err, "a deleted anchor falls back to the section's end")
	require.False(t, moved.Changed)
}

func TestDeleteWorkCreditGroupUngroupsItsCredits(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	ctx := admin.context(t)
	value := h.seedWork(t, "Ungrouped")
	group, err := h.service.CreateWorkCreditGroup(ctx, work.CreateWorkCreditGroupCommand{WorkID: value.ID, Name: "Group"})
	require.NoError(t, err)
	credit, err := h.service.AddWorkCredit(ctx, work.AddWorkCreditCommand{WorkID: value.ID, GroupID: &group.ID, Name: stringPointer("Member")})
	require.NoError(t, err)

	require.NoError(t, h.service.DeleteWorkCreditGroup(ctx, work.DeleteWorkCreditGroupCommand{GroupID: group.ID}))

	credits := h.credits(t, value.ID)
	require.Empty(t, credits.Groups)
	require.Equal(t, []string{"c:" + credit.Credit.ID}, orderKeys(credits.Order))
	require.Nil(t, h.store.StoredCredits(value.ID)[0].GroupID)
	require.Equal(t, 1, h.store.StoredCredits(value.ID)[0].SortOrder)

	err = h.service.DeleteWorkCreditGroup(ctx, work.DeleteWorkCreditGroupCommand{GroupID: group.ID})
	requireCode(t, err, connect.CodeNotFound, "credit_group not found: "+group.ID)
}

func TestUpdateWorkCreditGroupComparesTheNameItLocked(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	value := h.seedWork(t, "Renamed")
	group := h.store.PutCreditGroup(work.CreditGroup{WorkID: value.ID, Name: "Stale", SortOrder: 1})
	h.store.Before = map[string]func(){"LockCreditGroup": func() {
		h.store.Before = nil
		h.store.PutCreditGroup(work.CreditGroup{ID: group.ID, WorkID: value.ID, Name: "Concurrent", SortOrder: 1})
	}}

	renamed, err := h.service.UpdateWorkCreditGroup(admin.context(t), work.UpdateWorkCreditGroupCommand{GroupID: group.ID, Name: stringPointer("Stale")})

	require.NoError(t, err)
	require.Equal(t, "Stale", renamed.Name, "a request equal to the name read before the lock still renames the concurrent one")
	require.Len(t, h.audit.Records(), 1)
}

func TestUpdateAndDeleteWorkCreditKeepTheOrder(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	ctx := admin.context(t)
	value := h.seedWork(t, "Credit changes")
	group, err := h.service.CreateWorkCreditGroup(ctx, work.CreateWorkCreditGroupCommand{WorkID: value.ID, Name: "Group"})
	require.NoError(t, err)
	first, err := h.service.AddWorkCredit(ctx, work.AddWorkCreditCommand{WorkID: value.ID, Name: stringPointer("First")})
	require.NoError(t, err)
	second, err := h.service.AddWorkCredit(ctx, work.AddWorkCreditCommand{WorkID: value.ID, Name: stringPointer("Second")})
	require.NoError(t, err)

	updated, err := h.service.UpdateWorkCredit(ctx, work.UpdateWorkCreditCommand{CreditID: second.Credit.ID, GroupID: &group.ID, CreditRole: stringPointer("Role")})
	require.NoError(t, err)
	require.Equal(t, group.ID, *updated.Credit.GroupID)
	require.Equal(t, "Role", *updated.Credit.CreditRole)
	require.Equal(t, []string{"g:" + group.ID, "c:" + second.Credit.ID + "@" + group.ID, "c:" + first.Credit.ID}, orderKeys(h.credits(t, value.ID).Order))
	records := len(h.audit.Records())
	_, err = h.service.UpdateWorkCredit(ctx, work.UpdateWorkCreditCommand{CreditID: second.Credit.ID, CreditRole: stringPointer("Role")})
	require.NoError(t, err)
	require.Len(t, h.audit.Records(), records, "an unchanged credit is not audited")

	require.NoError(t, h.service.DeleteWorkCredit(ctx, work.DeleteWorkCreditCommand{CreditID: second.Credit.ID}))
	require.Equal(t, []string{"g:" + group.ID, "c:" + first.Credit.ID}, orderKeys(h.credits(t, value.ID).Order))
	err = h.service.DeleteWorkCredit(ctx, work.DeleteWorkCreditCommand{CreditID: second.Credit.ID})
	requireCode(t, err, connect.CodeNotFound, "credit not found: "+second.Credit.ID)
}

func TestListMyCreditedWorksListsTheCallersAndItsArtistsCredits(t *testing.T) {
	h := newHarness(t)
	caller := newPrincipal()
	h.members.summaries[caller.memberID] = &memberref.Summary{ID: caller.memberID, Nickname: "Me", Avatar: memberref.AssetFromReference(&mediaasset.Reference{URL: "avatar"})}
	h.store.PutArtist(work.CreditArtistRow{ID: "artist-1", Name: " ", ImageFileID: stringPointer("artist-image")}, caller.memberID)
	h.store.PutFileAsset("artist-image", "image", &mediaasset.Reference{URL: "artist"})
	mine := h.seedWork(t, "Mine", func(value *work.Work) { value.SourceLocale = "ko" })
	theirs := h.seedWork(t, "Theirs")
	h.store.PutCredit(work.Credit{WorkID: mine.ID, MemberID: &caller.memberID})
	h.store.PutCredit(work.Credit{WorkID: theirs.ID, ArtistID: stringPointer("artist-1")})
	h.store.PutCredit(work.Credit{WorkID: theirs.ID, MemberID: stringPointer(uuid.NewString())})

	page, err := h.service.ListMyCreditedWorks(caller.context(t), work.ListMyCreditedWorksQuery{})

	require.NoError(t, err)
	require.Equal(t, int32(2), page.Pagination.Total)
	byWork := map[string]work.CreditedWork{}
	for _, credited := range page.Works {
		byWork[credited.WorkID] = credited
	}
	require.Equal(t, work.CreditTypeMember, byWork[mine.ID].CreditType)
	require.Equal(t, "Me", byWork[mine.ID].CreditedAs)
	require.Equal(t, "avatar", optional.Value(byWork[mine.ID].Image).URL)
	require.Equal(t, "Mine", byWork[mine.ID].Title)
	require.Equal(t, "ko", byWork[mine.ID].SourceLocale)
	require.Equal(t, work.CreditTypeArtist, byWork[theirs.ID].CreditType)
	require.Equal(t, "Unknown Artist", byWork[theirs.ID].CreditedAs, "a blank Artist name reads as unknown")
	require.Equal(t, "artist", optional.Value(byWork[theirs.ID].Image).URL)

	_, err = h.service.ListMyCreditedWorks(t.Context(), work.ListMyCreditedWorksQuery{})
	requireCode(t, err, connect.CodeUnauthenticated, "")
}
