package page_test

import (
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/page/pagetest"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func versionSnapshot(t *testing.T, locale string, title, summary *string) json.RawMessage {
	t.Helper()
	encoded, err := page.EncodeVersionContentSnapshot(locale, title, summary, &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: locale,
		Base: &contentv1.PageSectionGraph{}, LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: locale},
	})
	require.NoError(t, err)
	return encoded
}

func TestListPageVersionsNamesTheirContributors(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Page")
	editor := h.member(newPrincipal())
	for number := int32(1); number <= 3; number++ {
		h.store.PutVersion(page.Version{PageID: stored.ID, Version: number, ContentSnapshot: versionSnapshot(t, "en", text("Page"), nil), ContributorMemberIDs: []string{editor.memberID}})
	}

	list, err := h.service.ListPageVersions(editor.context(t), page.ListPageVersionsQuery{PageID: stored.ID, Page: page.Paging{Limit: 2}})
	require.NoError(t, err)
	require.Equal(t, page.Pagination{Total: 3, Limit: 2, HasMore: true}, list.Pagination)
	require.Equal(t, int32(3), list.Versions[0].Version.Version)
	require.Equal(t, "en", list.Versions[0].SourceLocale)
	require.Equal(t, []page.Contributor{{MemberID: editor.memberID, Nickname: "Member " + editor.memberID[:8]}}, list.Versions[0].Contributors)

	view, err := policyv1.Page.View(stored.ID)
	require.NoError(t, err)
	h.checker.Deny(view, nil)
	_, err = h.service.ListPageVersions(editor.context(t), page.ListPageVersionsQuery{PageID: stored.ID})
	requireCode(t, err, connect.CodeNotFound, "page not found: "+stored.ID)
}

func TestRestorePageVersionRestoresTheSourceTitleAndSummary(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Current")
	editor := h.member(newPrincipal())
	version := h.store.PutVersion(page.Version{PageID: stored.ID, Version: 1, ContentSnapshot: versionSnapshot(t, "en", text("Restored"), text("Old summary"))})
	revision := h.revision(t, stored.ID)

	restored, err := h.service.RestorePageVersion(editor.context(t), page.RestorePageVersionCommand{PageID: stored.ID, VersionID: version.ID})

	require.NoError(t, err)
	require.Equal(t, "Restored", restored.Title)
	require.Equal(t, "Old summary", *restored.Summary)
	require.NotEqual(t, revision.String(), restored.Revision, "a title-only restore advances the revision")
	require.Equal(t, []pagetest.OGRequest{{PageID: stored.ID, Locale: "en", Reason: "page_version_restored"}}, h.store.OGRequests())
	update := h.store.Updates()[0]
	require.Equal(t, page.LocaleUpdate, update.Kind)
	require.Equal(t, []string{"title", "summary"}, update.Locale.Fields)
	require.Equal(t, []string{editor.memberID}, update.Locale.Contributors)
	require.Equal(t, sharedtelemetry.AuditPageUpdated, h.audit.Records()[0].Action)
	require.Equal(t, version.ID, h.audit.Records()[0].VersionID)

	// Restoring the same version again changes nothing.
	_, err = h.service.RestorePageVersion(editor.context(t), page.RestorePageVersionCommand{VersionID: version.ID})
	require.NoError(t, err)
	require.Len(t, h.audit.Records(), 1)
	require.Len(t, h.store.Updates(), 1)
}

func TestRestorePageVersionSwitchesToTheVersionSourceLocale(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("English")
	h.store.PutLocale(stored.ID, page.Locale{Locale: "ko", Title: text("한국어")})
	version := h.store.PutVersion(page.Version{PageID: stored.ID, Version: 1, ContentSnapshot: versionSnapshot(t, "ko", text("한국어"), nil)})

	restored, err := h.service.RestorePageVersion(h.member(newPrincipal()).context(t), page.RestorePageVersionCommand{VersionID: version.ID})

	require.NoError(t, err)
	require.Equal(t, "ko", restored.Page.SourceLocale)
	require.Equal(t, []string{"sourceLocale"}, h.store.Updates()[0].Locale.Fields)
	english, exists := h.store.StoredLocale(stored.ID, "en")
	require.True(t, exists, "the previous source locale stays")
	require.Equal(t, "English", *english.Title)
}

func TestRestorePageVersionRejectsMissingForeignAndCorruptVersions(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Page")
	other := h.seedPage("Other")
	editor := h.member(newPrincipal())
	foreign := h.store.PutVersion(page.Version{PageID: other.ID, Version: 1, ContentSnapshot: versionSnapshot(t, "en", text("Other"), nil)})
	corrupt := h.store.PutVersion(page.Version{PageID: stored.ID, Version: 1, ContentSnapshot: json.RawMessage(`{"schemaVersion":9}`)})

	missing := uuid.NewString()
	_, err := h.service.RestorePageVersion(editor.context(t), page.RestorePageVersionCommand{PageID: stored.ID, VersionID: missing})
	requireCode(t, err, connect.CodeNotFound, "page_version not found: "+missing)
	_, err = h.service.RestorePageVersion(editor.context(t), page.RestorePageVersionCommand{PageID: stored.ID, VersionID: foreign.ID})
	requireCode(t, err, connect.CodeNotFound, "page_version not found: "+foreign.ID)
	_, err = h.service.RestorePageVersion(editor.context(t), page.RestorePageVersionCommand{PageID: stored.ID, VersionID: corrupt.ID})
	requireCode(t, err, connect.CodeFailedPrecondition, "Page version typed snapshot is invalid")

	edit, err := policyv1.Page.Edit(other.ID)
	require.NoError(t, err)
	h.checker.Deny(edit, nil)
	_, err = h.service.RestorePageVersion(editor.context(t), page.RestorePageVersionCommand{VersionID: foreign.ID})
	requireCode(t, err, connect.CodeNotFound, "page not found: "+other.ID)
	require.Empty(t, h.audit.Records())
}
