package label_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func set(value string) *entityparticipant.StringMutation {
	return &entityparticipant.StringMutation{Set: &value}
}

func clear(value bool) *entityparticipant.StringMutation {
	return &entityparticipant.StringMutation{Clear: &value}
}

// roomFixture is a Label with a source title and a contributor of its
// collaboration room.
type roomFixture struct {
	h           *harness
	label       label.Label
	contributor principal
}

func newRoomFixture(t *testing.T) roomFixture {
	t.Helper()
	h := newHarness(t)
	return roomFixture{h: h, label: h.seedLabel(t, "Room"), contributor: h.member(newPrincipal())}
}

func (f roomFixture) metadata(t *testing.T, update *label.DocumentMetadataUpdate, expected string) (contentblock.AdvanceResult, error) {
	t.Helper()
	return f.h.service.UpdateLabelDocumentMetadata(f.contributor.context(t), label.UpdateLabelDocumentMetadataCommand{
		LabelID: f.label.ID, ExpectedRevision: expected, Update: update, ContributorMemberIDs: []string{f.contributor.memberID},
	})
}

func TestUpdateLabelDocumentMetadataSavesTheRowAndMovesTheParent(t *testing.T) {
	f := newRoomFixture(t)
	parent := f.h.seedLabel(t, "Parent")
	seeded, _ := f.h.store.Label(f.label.ID)
	seeded.SocialLinks = map[string]string{"site": "https://example.com"}
	f.h.store.PutLabel(seeded)
	before := f.h.revision(t, f.label.ID)

	advanced, err := f.metadata(t, &label.DocumentMetadataUpdate{
		Slug: set(" room "), CountryCode: set("KR"), Website: clear(true), ParentLabelID: set(parent.ID),
		SocialLinksPresent: true,
	}, before.String())

	require.NoError(t, err)
	require.True(t, advanced.Changed)
	require.NotEqual(t, before, advanced.DocumentRevision)
	stored, _ := f.h.store.Label(f.label.ID)
	require.Equal(t, "room", *stored.Slug)
	require.Equal(t, "KR", *stored.CountryCode)
	require.Nil(t, stored.Website)
	require.Equal(t, parent.ID, *stored.ParentLabelID)
	require.Equal(t, map[string]string{}, stored.SocialLinks, "present but empty social links replace the map")
	touch, err := policyv1.Label.TouchParent(f.label.ID, parent.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch}}, f.h.checker.Applied())
	require.Empty(t, f.h.audit.Records(), "document-metadata saves have never been audited")
	require.Equal(t, mutation.StepRelationships, f.h.units.Steps()[len(f.h.units.Steps())-2])

	again, err := f.metadata(t, &label.DocumentMetadataUpdate{Slug: set("room")}, advanced.DocumentRevision.String())
	require.NoError(t, err)
	require.False(t, again.Changed, "current values are a no-op")
	require.Len(t, f.h.checker.Applied(), 1)
}

func TestUpdateLabelDocumentMetadataClearsOrKeepsTheParent(t *testing.T) {
	f := newRoomFixture(t)
	parent := f.h.seedLabel(t, "Parent")
	stored, _ := f.h.store.Label(f.label.ID)
	stored.ParentLabelID = &parent.ID
	f.h.store.PutLabel(stored)

	kept, err := f.metadata(t, &label.DocumentMetadataUpdate{ParentLabelID: set(parent.ID)}, f.h.revision(t, f.label.ID).String())
	require.NoError(t, err)
	require.False(t, kept.Changed, "the current parent is a no-op")
	require.Empty(t, f.h.checker.Applied())

	cleared, err := f.metadata(t, &label.DocumentMetadataUpdate{ParentLabelID: clear(true)}, kept.DocumentRevision.String())
	require.NoError(t, err)
	require.True(t, cleared.Changed)
	stored, _ = f.h.store.Label(f.label.ID)
	require.Nil(t, stored.ParentLabelID)
	remove, err := policyv1.Label.DeleteParent(f.label.ID, parent.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{remove}}, f.h.checker.Applied())
}

func TestUpdateLabelDocumentMetadataRejections(t *testing.T) {
	for _, test := range []struct {
		name     string
		prepare  func(*testing.T, roomFixture) *label.DocumentMetadataUpdate
		expected func(roomFixture) string
		code     connect.Code
		message  string
	}{
		{name: "nil update", prepare: func(*testing.T, roomFixture) *label.DocumentMetadataUpdate { return nil },
			code: connect.CodeInvalidArgument, message: "update is required"},
		{name: "empty update", prepare: func(*testing.T, roomFixture) *label.DocumentMetadataUpdate { return &label.DocumentMetadataUpdate{} },
			code: connect.CodeInvalidArgument, message: "update: at least one metadata value is required"},
		{name: "blank set", prepare: func(*testing.T, roomFixture) *label.DocumentMetadataUpdate {
			return &label.DocumentMetadataUpdate{Website: set("  ")}
		}, code: connect.CodeInvalidArgument, message: "website: set value must not be empty; use clear instead"},
		{name: "clear false", prepare: func(*testing.T, roomFixture) *label.DocumentMetadataUpdate {
			return &label.DocumentMetadataUpdate{CountryCode: clear(false)}
		}, code: connect.CodeInvalidArgument, message: "country_code: clear must be true"},
		{name: "no operation", prepare: func(*testing.T, roomFixture) *label.DocumentMetadataUpdate {
			return &label.DocumentMetadataUpdate{Slug: &entityparticipant.StringMutation{}}
		}, code: connect.CodeInvalidArgument, message: "slug: set or clear is required"},
		{name: "slug with a slash", prepare: func(*testing.T, roomFixture) *label.DocumentMetadataUpdate {
			return &label.DocumentMetadataUpdate{Slug: set("a/b")}
		}, code: connect.CodeInvalidArgument, message: "slug: must not contain '/'"},
		{name: "invalid parent ID", prepare: func(*testing.T, roomFixture) *label.DocumentMetadataUpdate {
			return &label.DocumentMetadataUpdate{ParentLabelID: set("not-a-uuid")}
		}, code: connect.CodeInvalidArgument, message: "parent_label_id: must be a valid Label ID"},
		{name: "malformed revision", prepare: func(*testing.T, roomFixture) *label.DocumentMetadataUpdate {
			return &label.DocumentMetadataUpdate{Slug: set("room")}
		}, expected: func(roomFixture) string { return "not-a-revision" },
			code: connect.CodeInvalidArgument, message: "expected_revision: must be a UUID"},
		{name: "stale revision", prepare: func(*testing.T, roomFixture) *label.DocumentMetadataUpdate {
			return &label.DocumentMetadataUpdate{Slug: set("room")}
		}, expected: func(roomFixture) string { return uuid.NewString() },
			code: connect.CodeFailedPrecondition, message: "Label document changed since it was loaded; reload before saving"},
		{name: "self parent", prepare: func(_ *testing.T, f roomFixture) *label.DocumentMetadataUpdate {
			return &label.DocumentMetadataUpdate{ParentLabelID: set(f.label.ID)}
		}, code: connect.CodeInvalidArgument, message: "parent_label_id: cannot reference the same label"},
		{name: "missing parent", prepare: func(*testing.T, roomFixture) *label.DocumentMetadataUpdate {
			return &label.DocumentMetadataUpdate{ParentLabelID: set(uuid.NewString())}
		}, code: connect.CodeInvalidArgument, message: "parent_label_id: label does not exist"},
		{name: "descendant parent", prepare: func(t *testing.T, f roomFixture) *label.DocumentMetadataUpdate {
			child := f.h.seedLabel(t, "Child", func(child *label.Label) { child.ParentLabelID = &f.label.ID })
			return &label.DocumentMetadataUpdate{ParentLabelID: set(child.ID)}
		}, code: connect.CodeInvalidArgument, message: "parent_label_id: would create a label hierarchy cycle"},
		{name: "taken slug", prepare: func(t *testing.T, f roomFixture) *label.DocumentMetadataUpdate {
			f.h.seedLabel(t, "Other", func(other *label.Label) { other.Slug = stringPointer("taken") })
			return &label.DocumentMetadataUpdate{Slug: set("taken")}
		}, code: connect.CodeAlreadyExists, message: "label with slug 'taken' already exists"},
		{name: "revoked contributor", prepare: func(_ *testing.T, f roomFixture) *label.DocumentMetadataUpdate {
			f.h.store.RevokeRoom(f.label.ID)
			return &label.DocumentMetadataUpdate{Slug: set("room")}
		}, code: connect.CodePermissionDenied},
		{name: "archived lifecycle", prepare: func(_ *testing.T, f roomFixture) *label.DocumentMetadataUpdate {
			stored, _ := f.h.store.Label(f.label.ID)
			stored.Status = "LABEL_STATUS_ARCHIVED"
			f.h.store.PutLabel(stored)
			return &label.DocumentMetadataUpdate{Slug: set("room")}
		}, code: connect.CodeFailedPrecondition, message: "Label lifecycle no longer permits collaboration editing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newRoomFixture(t)
			update := test.prepare(t, f)
			expected := f.h.revision(t, f.label.ID).String()
			if test.expected != nil {
				expected = test.expected(f)
			}
			before, _ := f.h.store.Label(f.label.ID)

			_, err := f.metadata(t, update, expected)

			requireCode(t, err, test.code, test.message)
			after, _ := f.h.store.Label(f.label.ID)
			require.Equal(t, before, after)
			require.Empty(t, f.h.checker.Applied())
		})
	}
	t.Run("missing label", func(t *testing.T) {
		f := newRoomFixture(t)
		_, err := f.h.service.UpdateLabelDocumentMetadata(f.contributor.context(t), label.UpdateLabelDocumentMetadataCommand{
			LabelID: "00000000-0000-4000-8000-000000000005", ExpectedRevision: uuid.NewString(),
			Update: &label.DocumentMetadataUpdate{Slug: set("room")}, ContributorMemberIDs: []string{f.contributor.memberID},
		})
		requireCode(t, err, connect.CodeNotFound, "label not found: 00000000-0000-4000-8000-000000000005")
	})
}

func TestUpdateLabelLocaleMetadataSavesAndAuditsTheSourceTitle(t *testing.T) {
	f := newRoomFixture(t)
	before := f.h.revision(t, f.label.ID)
	title := "  Renamed  "

	update, err := f.h.service.UpdateLabelLocaleMetadata(f.contributor.context(t), label.UpdateLabelLocaleMetadataCommand{
		LabelID: f.label.ID, Locale: "en", Title: &title, ExpectedRevision: before.String(),
		ContributorMemberIDs: []string{f.contributor.memberID},
	})

	require.NoError(t, err)
	require.True(t, update.Advance.Changed)
	require.Equal(t, []string{"en"}, update.ChangedLocales)
	stored, _ := f.h.store.Locale(f.label.ID, "en")
	require.Equal(t, "Renamed", *stored.Title)
	require.Equal(t, "label_source_title_saved", f.h.store.OGRequests()[0].Reason)
	records := f.h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, records[0].ItemOperation)
	updates := f.h.store.Updates()
	require.Len(t, updates, 1)
	require.Equal(t, label.LocaleUpdate, updates[0].Kind)
	require.Equal(t, label.LocaleChange{
		Fields: []string{"title"}, DocumentRevision: update.Advance.DocumentRevision.String(),
		Contributors: []string{f.contributor.memberID}, Source: label.SourceCollaboration, Locale: "en", DocumentStateChanged: true,
	}, updates[0].Locale)

	_, err = f.h.service.UpdateLabelLocaleMetadata(f.contributor.context(t), label.UpdateLabelLocaleMetadataCommand{
		LabelID: f.label.ID, Locale: "ko", Title: &title, ExpectedRevision: update.Advance.DocumentRevision.String(),
		ContributorMemberIDs: []string{f.contributor.memberID},
	})
	requireCode(t, err, connect.CodeInvalidArgument, "Label title is source-owned and cannot be changed from a target locale room")
	_, err = f.h.service.UpdateLabelLocaleMetadata(f.contributor.context(t), label.UpdateLabelLocaleMetadataCommand{
		LabelID: f.label.ID, Locale: "en", Title: &title, ExpectedRevision: update.Advance.DocumentRevision.String(),
		ContributorMemberIDs: []string{uuid.NewString()},
	})
	requireCode(t, err, connect.CodeInvalidArgument, "contributor_member_ids: contains a Member that does not exist")
	require.Len(t, f.h.audit.Records(), 1)
}

func paragraphBatch(revision uuid.UUID, contributor, locale string) *contentv1.RichTextBlockMutationBatch {
	blockID := uuid.NewString()
	return &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT,
		ExpectedRevision:        revision.String(), ContributorMemberIds: []string{contributor},
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: locale,
			Mutations: []*contentv1.RichTextBlockLocaleMutation{{Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{
				Upsert: &contentv1.UpsertRichTextBlockLocale{Block: &contentv1.RichTextBlockLocale{
					BlockId: blockID,
					Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
						Props:   &contentv1.ParagraphLocaleProps{},
						Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "text"}}}},
					}},
				}},
			}}},
		}},
	}
}

func TestApplyLabelBlockBatchAppliesTheSourceRoomAndSignalsTheChange(t *testing.T) {
	f := newRoomFixture(t)
	before := f.h.revision(t, f.label.ID)

	result, err := f.h.service.ApplyLabelBlockBatch(f.contributor.context(t), label.ApplyLabelBlockBatchCommand{
		LabelID: f.label.ID, Locale: "en", Batch: paragraphBatch(before, f.contributor.memberID, "en"),
	})

	require.NoError(t, err)
	require.True(t, result.Content.Changed)
	require.NotEqual(t, before, result.Content.DocumentRevision)
	require.Equal(t, "en", result.SourceLocale)
	require.Equal(t, "label_source_document_saved", f.h.store.OGRequests()[0].Reason)
	require.Len(t, f.h.audit.Records(), 1)
	updates := f.h.store.Updates()
	require.Len(t, updates, 1)
	require.Equal(t, label.LocaleChange{
		Fields: []string{"content"}, DocumentRevision: result.Content.DocumentRevision.String(),
		Contributors: []string{f.contributor.memberID}, Source: label.SourceCollaboration, Locale: "en", DocumentStateChanged: true,
	}, updates[0].Locale)

	_, err = f.h.service.ApplyLabelBlockBatch(f.contributor.context(t), label.ApplyLabelBlockBatchCommand{
		LabelID: f.label.ID, Locale: "en", Batch: paragraphBatch(result.Content.DocumentRevision, f.contributor.memberID, "ko"),
	})
	requireCode(t, err, connect.CodeInvalidArgument, "Label source room mutation must match the current source locale")
	_, err = f.h.service.ApplyLabelBlockBatch(f.contributor.context(t), label.ApplyLabelBlockBatchCommand{
		LabelID: f.label.ID, Locale: "en", Batch: paragraphBatch(before, f.contributor.memberID, "en"),
	})
	requireCode(t, err, connect.CodeFailedPrecondition, "Label document changed since it was loaded; reload before saving")
	_, err = f.h.service.ApplyLabelBlockBatch(f.contributor.context(t), label.ApplyLabelBlockBatchCommand{LabelID: f.label.ID, Locale: "en"})
	requireCode(t, err, connect.CodeInvalidArgument, "batch is required")
	require.Len(t, f.h.store.Updates(), 1)
}

func TestApplyLabelBlockBatchCreatesATargetLocaleWithoutAdvancingTheSharedRevision(t *testing.T) {
	f := newRoomFixture(t)
	before := f.h.revision(t, f.label.ID)

	_, err := f.h.service.ApplyLabelBlockBatch(f.contributor.context(t), label.ApplyLabelBlockBatchCommand{
		LabelID: f.label.ID, Locale: "ko", Batch: paragraphBatch(before, f.contributor.memberID, "ko"),
	})

	requireCode(t, err, connect.CodeFailedPrecondition, "")
	_, exists := f.h.store.Locale(f.label.ID, "ko")
	require.False(t, exists, "a target room cannot create its locale")
	require.Equal(t, before, f.h.revision(t, f.label.ID))
}

func TestLoadLabelBlockDocumentResolvesTheSessionAndRequiresEdit(t *testing.T) {
	f := newRoomFixture(t)
	user := f.contributor.user()
	f.h.store.PutSession("session", user)

	room, err := f.h.service.LoadLabelBlockDocument(t.Context(), label.LoadLabelBlockDocumentQuery{LabelID: f.label.ID, Locale: "en", SessionID: "session"})
	require.NoError(t, err)
	require.True(t, room.LocaleExists)
	require.Equal(t, "Room", *room.SourceMetadata.Title)
	require.Nil(t, room.TargetRevision)

	target, err := f.h.service.LoadLabelBlockDocument(t.Context(), label.LoadLabelBlockDocumentQuery{LabelID: f.label.ID, Locale: "ko", SessionID: "session"})
	require.NoError(t, err)
	require.False(t, target.LocaleExists)

	f.h.store.PutLocale(f.label.ID, label.LocaleTitle{Locale: "ko", UpdatedAt: time.Now()})
	target, err = f.h.service.LoadLabelBlockDocument(t.Context(), label.LoadLabelBlockDocumentQuery{LabelID: f.label.ID, Locale: "ko", SessionID: "session"})
	require.NoError(t, err)
	require.True(t, target.LocaleExists)
	require.NotNil(t, target.TargetRevision)

	_, err = f.h.service.LoadLabelBlockDocument(t.Context(), label.LoadLabelBlockDocumentQuery{LabelID: f.label.ID, Locale: "en", SessionID: "unknown"})
	requireCode(t, err, connect.CodeUnauthenticated, "authentication required")
	banned := f.contributor.user()
	banned.Banned = true
	f.h.store.PutSession("banned", banned)
	_, err = f.h.service.LoadLabelBlockDocument(t.Context(), label.LoadLabelBlockDocumentQuery{LabelID: f.label.ID, Locale: "en", SessionID: "banned"})
	requireCode(t, err, connect.CodePermissionDenied, "account is banned")
	edit, err := policyv1.Label.Edit(f.label.ID)
	require.NoError(t, err)
	f.h.checker.Deny(edit, nil)
	_, err = f.h.service.LoadLabelBlockDocument(t.Context(), label.LoadLabelBlockDocumentQuery{LabelID: f.label.ID, Locale: "en", SessionID: "session"})
	requireCode(t, err, connect.CodePermissionDenied, "no permission to edit this label")
	_, err = f.h.service.LoadLabelBlockDocument(t.Context(), label.LoadLabelBlockDocumentQuery{LabelID: f.label.ID, Locale: " ", SessionID: "session"})
	requireCode(t, err, connect.CodeInvalidArgument, "locale is required")
}

func TestTranslationDeliveryAppliesToADraftLabelAndRejectsADeletedOne(t *testing.T) {
	f := newRoomFixture(t)
	job := label.TranslationJob{EntityType: "label", EntityID: f.label.ID, TargetLocale: "ko", RequestedByMemberID: f.contributor.memberID}
	candidate := &translation.Candidate{
		ContentDocumentRevision:   f.h.revision(t, f.label.ID).String(),
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"},
	}

	require.NoError(t, label.ApplyTypedTranslationCandidate(f.contributor.context(t), label.Joined{Store: f.h.store, Audit: f.h.audit}, job, candidate, translation.EntryWrite{Now: time.Now()}),
		"an already-submitted translation job may apply to a draft Label")
	_, exists := f.h.store.Locale(f.label.ID, "ko")
	require.True(t, exists)
	require.Equal(t, []sharedtelemetry.AuditItemOperation{sharedtelemetry.AuditItemOperationCreated}, itemOperations(f.h))

	require.NoError(t, f.h.store.DeleteLabel(t.Context(), f.label.ID))
	err := label.ApplyTypedTranslationCandidate(f.contributor.context(t), label.Joined{Store: f.h.store, Audit: f.h.audit}, job, candidate, translation.EntryWrite{Now: time.Now()})
	requireCode(t, err, connect.CodeNotFound, "label not found: "+f.label.ID)
}

func itemOperations(h *harness) []sharedtelemetry.AuditItemOperation {
	var operations []sharedtelemetry.AuditItemOperation
	for _, record := range h.audit.Records() {
		operations = append(operations, record.ItemOperation)
	}
	return operations
}

func TestRequireLockedSourceLocaleEditLocksTheLabelAndThePrincipal(t *testing.T) {
	f := newRoomFixture(t)
	ctx := f.contributor.context(t)
	active := func(*auth.UserInfo) (bool, error) { return true, nil }
	inactive := func(*auth.UserInfo) (bool, error) { return false, nil }

	require.NoError(t, label.RequireLockedSourceLocaleEdit(ctx, label.Joined{Store: f.h.store}, f.h.checker, active, f.label.ID))
	err := label.RequireLockedSourceLocaleEdit(ctx, label.Joined{Store: f.h.store}, f.h.checker, inactive, f.label.ID)
	requireCode(t, err, connect.CodeNotFound, "label not found: "+f.label.ID)
	err = label.RequireLockedSourceLocaleEdit(ctx, label.Joined{Store: f.h.store}, f.h.checker, active, "00000000-0000-4000-8000-000000000006")
	requireCode(t, err, connect.CodeNotFound, "label not found: 00000000-0000-4000-8000-000000000006")
}
