package work_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/work"
	"github.com/echovisionlab/geul-api/internal/work/worktest"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// checkpoint records the Work's source room as a version on behalf of
// contributor and returns it.
func (h *harness) checkpoint(t *testing.T, workID string, contributor principal) work.VersionCheckpoint {
	t.Helper()
	checkpoint, err := h.service.CreateWorkVersionCheckpoint(collabContext(t), work.CreateWorkVersionCheckpointCommand{
		WorkID: workID, Locale: h.stored(t, workID).SourceLocale, ExpectedRevision: h.revision(t, workID).String(),
		ContributorMemberIDs: []string{contributor.memberID},
	})
	require.NoError(t, err)
	return checkpoint
}

func TestCreateWorkVersionCheckpointRecordsOnlyAChangedSourceRoom(t *testing.T) {
	h := newHarness(t)
	contributor := newPrincipal()
	h.store.PutMember(contributor.memberID, worktest.Member{Nickname: "Writer"})
	value := h.seedWork(t, "Checkpointed")

	first := h.checkpoint(t, value.ID, contributor)
	require.NotNil(t, first.Created)
	require.Equal(t, int32(1), first.Created.Version)
	require.Equal(t, []string{contributor.memberID}, first.Created.ContributorMemberIDs)
	require.Equal(t, h.revision(t, value.ID).String(), first.Revision, "a checkpoint does not advance the revision")
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditWorkUpdated, records[0].Action)
	require.Equal(t, sharedtelemetry.RecordActor{Kind: sharedtelemetry.ActorKindSystem, Service: "geul-collab"}, records[0].RecordActor,
		"the collaboration service records the version, not a Member")
	require.Equal(t, []string{"version"}, records[0].ChangedFields)
	require.Equal(t, first.Created.ID, records[0].VersionID)
	require.Equal(t, []string{contributor.memberID}, records[0].ContributorMemberIDs)

	same := h.checkpoint(t, value.ID, contributor)
	require.Nil(t, same.Created, "the same source room is no new version")
	require.Len(t, h.audit.Records(), 1)

	h.store.PutLocale(value.ID, work.LocaleMetadata{Locale: "en", Title: stringPointer("Retitled")})
	second := h.checkpoint(t, value.ID, contributor)
	require.NotNil(t, second.Created)
	require.Equal(t, int32(2), second.Created.Version)

	page, err := h.service.ListWorkVersions(contributor.context(t), work.ListWorkVersionsQuery{WorkID: value.ID})
	require.NoError(t, err)
	require.Equal(t, work.Pagination{Total: 2, Limit: 20, Offset: 0}, page.Pagination)
	require.Equal(t, "Retitled", page.Versions[0].Title)
	require.Equal(t, []work.VersionContributor{{MemberID: contributor.memberID, Nickname: "Writer"}}, page.Versions[1].Contributors)
	require.Len(t, page.Versions[0].CanonicalHash, 64)
}

func TestCreateWorkVersionCheckpointRejectsATargetRoomAndUnsortedContributors(t *testing.T) {
	h := newHarness(t)
	contributor := newPrincipal()
	h.store.PutMember(contributor.memberID, worktest.Member{Nickname: "Writer"})
	value := h.seedWork(t, "Checkpointed")
	revision := h.revision(t, value.ID).String()

	_, err := h.service.CreateWorkVersionCheckpoint(collabContext(t), work.CreateWorkVersionCheckpointCommand{
		WorkID: value.ID, Locale: "ko", ExpectedRevision: revision, ContributorMemberIDs: []string{contributor.memberID},
	})
	requireCode(t, err, connect.CodeInvalidArgument, "locale: Work version checkpoints are source-locale only")
	_, err = h.service.CreateWorkVersionCheckpoint(collabContext(t), work.CreateWorkVersionCheckpointCommand{
		WorkID: value.ID, Locale: "en", ExpectedRevision: revision, ContributorMemberIDs: []string{"b", "a"},
	})
	requireCode(t, err, connect.CodeInvalidArgument, "contributor_member_ids: must be sorted and unique")
	h.store.RevokeRoom(value.ID)
	_, err = h.service.CreateWorkVersionCheckpoint(collabContext(t), work.CreateWorkVersionCheckpointCommand{
		WorkID: value.ID, Locale: "en", ExpectedRevision: revision, ContributorMemberIDs: []string{contributor.memberID},
	})
	requireCode(t, err, connect.CodePermissionDenied, "")
	require.Empty(t, h.store.Versions(value.ID))
}

func TestRestoreWorkVersionRestoresTheSourceLocaleTitleAndSummary(t *testing.T) {
	type want struct {
		fields    []string
		ogLocales []string
	}
	for name, test := range map[string]struct {
		drift func(h *harness, workID string)
		want  *want
	}{
		"metadata": {
			drift: func(h *harness, workID string) {
				h.store.PutLocale(workID, work.LocaleMetadata{Locale: "en", Title: stringPointer("Drifted"), Summary: stringPointer("Drifted summary")})
			},
			want: &want{fields: []string{"title", "summary"}, ogLocales: []string{"en"}},
		},
		"summary only": {
			drift: func(h *harness, workID string) {
				h.store.PutLocale(workID, work.LocaleMetadata{Locale: "en", Title: stringPointer("Restored"), Summary: stringPointer("Drifted summary")})
			},
			want: &want{fields: []string{"summary"}},
		},
		"source locale switch": {
			drift: func(h *harness, workID string) {
				value, _ := h.store.Stored(workID)
				value.SourceLocale = "ko"
				h.store.PutWork(value)
				h.store.PutLocale(workID, work.LocaleMetadata{Locale: "ko", Title: stringPointer("Korean source")})
			},
			want: &want{fields: []string{"sourceLocale"}, ogLocales: []string{"en"}},
		},
		"unchanged": {drift: func(*harness, string) {}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			admin := newPrincipal()
			h.store.PutMember(admin.memberID, worktest.Member{Nickname: "Admin"})
			value := h.seedWork(t, "Restored")
			version := h.checkpoint(t, value.ID, admin).Created
			test.drift(h, value.ID)
			before := h.stored(t, value.ID)
			revision := h.revision(t, value.ID)
			audits, updates := len(h.audit.Records()), len(h.store.Updates())

			restored, err := h.service.RestoreWorkVersion(admin.context(t), work.RestoreWorkVersionCommand{WorkID: value.ID, VersionID: version.ID})

			require.NoError(t, err)
			require.Equal(t, "Restored", restored.Title)
			require.Nil(t, restored.Summary)
			require.Equal(t, "en", h.stored(t, value.ID).SourceLocale)
			if test.want == nil {
				require.Equal(t, revision, h.revision(t, value.ID))
				require.Equal(t, before.UpdatedAt, h.stored(t, value.ID).UpdatedAt)
				require.Len(t, h.audit.Records(), audits)
				require.Len(t, h.store.Updates(), updates)
				return
			}
			require.NotEqual(t, revision, h.revision(t, value.ID))
			require.Equal(t, h.revision(t, value.ID).String(), restored.Revision)
			require.Equal(t, []work.ContentUpdate{{WorkID: value.ID, Kind: work.LocaleUpdate, Locale: work.LocaleChange{
				Fields: test.want.fields, DocumentRevision: restored.Revision, Contributors: []string{admin.memberID},
				Source: work.SourceManage, Locale: "en", DocumentStateChanged: true,
			}}}, h.store.Updates()[updates:])
			var ogLocales []string
			for _, request := range h.store.OGRequests() {
				if request.Reason == "work_version_restored" {
					ogLocales = append(ogLocales, request.Locale)
				}
			}
			require.Equal(t, test.want.ogLocales, ogLocales)
			records := h.audit.Records()[audits:]
			require.Len(t, records, 1)
			require.Equal(t, []string{"version_restore"}, records[0].ChangedFields)
			require.Equal(t, version.ID, records[0].VersionID)
		})
	}
}

func TestRestoreWorkVersionPromotesItsSourceLocaleAndLeavesOtherLocales(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	value := h.seedWork(t, "English source")
	h.store.PutLocale(value.ID, work.LocaleMetadata{Locale: "ko", Title: stringPointer("Stored Korean target")})
	h.store.PutLocale(value.ID, work.LocaleMetadata{Locale: "fr", Title: stringPointer("Stored French target")})
	english, _ := h.store.Locale(value.ID, "en")
	french, _ := h.store.Locale(value.ID, "fr")
	document := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Profile: contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
		Locale: "ko", Base: &contentv1.RichTextBlockGraph{}, LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"},
	}
	snapshot, err := work.EncodeVersionContentSnapshot("ko", stringPointer("Restored Korean source"), stringPointer("Restored Korean summary"), document)
	require.NoError(t, err)
	version := h.store.PutVersion(work.Version{WorkID: value.ID, Version: 1, ContentSnapshot: snapshot})

	restored, err := h.service.RestoreWorkVersion(admin.context(t), work.RestoreWorkVersionCommand{WorkID: value.ID, VersionID: version.ID})

	require.NoError(t, err)
	require.Equal(t, "Restored Korean source", restored.Title)
	require.Equal(t, "ko", h.stored(t, value.ID).SourceLocale)
	korean, _ := h.store.Locale(value.ID, "ko")
	require.Equal(t, "Restored Korean summary", *korean.Summary)
	after, _ := h.store.Locale(value.ID, "en")
	require.Equal(t, english, after, "the former source row stays as it was")
	after, _ = h.store.Locale(value.ID, "fr")
	require.Equal(t, french, after)
	_, ok := h.store.Locale(value.ID, "ja")
	require.False(t, ok)
	require.Contains(t, h.store.OGRequests(), worktest.OGRequest{WorkID: value.ID, Locale: "ko", Reason: "work_version_restored"})
}

func TestRestoreWorkVersionRejectsAnUnrestorableVersion(t *testing.T) {
	h := newHarness(t)
	admin := newPrincipal()
	h.store.PutMember(admin.memberID, worktest.Member{Nickname: "Admin"})
	value := h.seedWork(t, "Restored")
	other := h.seedWork(t, "Other")
	version := h.checkpoint(t, value.ID, admin).Created
	invalid := h.store.PutVersion(work.Version{WorkID: value.ID, Version: 9, ContentSnapshot: []byte(`{"schemaVersion":1}`)})
	untitled := h.store.PutVersion(work.Version{WorkID: value.ID, Version: 10, ContentSnapshot: withoutTitle(t, version.ContentSnapshot)})
	missing := uuid.NewString()
	edit, err := policyv1.Work.Edit(other.ID)
	require.NoError(t, err)
	otherVersion := h.checkpoint(t, other.ID, admin).Created
	h.checker.Deny(edit, nil)

	for name, test := range map[string]struct {
		command work.RestoreWorkVersionCommand
		code    connect.Code
		message string
	}{
		"unknown version":         {work.RestoreWorkVersionCommand{WorkID: value.ID, VersionID: missing}, connect.CodeNotFound, "work_version not found: " + missing},
		"version of another work": {work.RestoreWorkVersionCommand{WorkID: other.ID, VersionID: version.ID}, connect.CodeNotFound, "work_version not found: " + version.ID},
		"invalid snapshot":        {work.RestoreWorkVersionCommand{WorkID: value.ID, VersionID: invalid.ID}, connect.CodeFailedPrecondition, "Work Version typed snapshot is invalid"},
		"snapshot without title":  {work.RestoreWorkVersionCommand{WorkID: value.ID, VersionID: untitled.ID}, connect.CodeFailedPrecondition, "Work Version source title is invalid"},
		"without edit":            {work.RestoreWorkVersionCommand{WorkID: other.ID, VersionID: otherVersion.ID}, connect.CodeNotFound, "work not found: " + other.ID},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := h.service.RestoreWorkVersion(admin.context(t), test.command)
			requireCode(t, err, test.code, test.message)
		})
	}
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
}

// withoutTitle is a version snapshot whose source title is null.
func withoutTitle(t *testing.T, snapshot []byte) []byte {
	t.Helper()
	envelope, document, err := work.DecodeVersionContentSnapshot(snapshot)
	require.NoError(t, err)
	encoded, err := work.EncodeVersionContentSnapshot(envelope.SourceLocale, nil, nil, document)
	require.NoError(t, err)
	return encoded
}
