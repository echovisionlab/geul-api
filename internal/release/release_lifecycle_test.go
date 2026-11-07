package release_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/release/releasetest"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func compactDocument(sourceLocale, body string) *contentv1.RichTextDocument {
	blockID := uuid.NewString()
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Profile: contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT,
		SourceLocale: sourceLocale,
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block:     &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}}},
			Placement: &contentv1.ContentBlockPlacement{Index: 0},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{Locale: sourceLocale, Blocks: []*contentv1.RichTextBlockLocale{{
			BlockId: blockID, Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
				Props:   &contentv1.ParagraphLocaleProps{},
				Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: body}}}},
			}},
		}}}},
	}
}

func TestCreateReleaseStoresTheDraftItsTitleAndPolicy(t *testing.T) {
	h := newHarness(t)
	created, err := h.service.CreateRelease(h.ctx(t), release.CreateReleaseCommand{
		Title: "  New release  ", SourceLocale: "en", Type: release.TypeEP, Slug: text("new-release"),
		Document: compactDocument("en", "Body"),
	})
	require.NoError(t, err)
	require.Equal(t, "New release", created.Title)
	require.Equal(t, release.StatusDraft, created.Release.Status)
	require.Equal(t, release.TypeEP, created.Release.Type)
	require.Equal(t, "en", created.Release.SourceLocale)
	require.Equal(t, h.revision(t, created.Release.ID).String(), created.Revision)
	metadata, _, ok := h.store.Locale(created.Release.ID, "en")
	require.True(t, ok)
	require.Equal(t, "New release", *metadata.Title)
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditReleaseCreated}, auditActions(h.audit))
	touch := policyv1.Release.TouchPolicy
	applied, err := touch(created.Release.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{applied}}, h.checker.Applied())
	require.Equal(t, mutation.StepCommit, lastStep(t, h.units))
}

func TestCreateReleaseRejectsInputBeforeAnyWrite(t *testing.T) {
	h := newHarness(t)
	_, err := h.service.CreateRelease(h.ctx(t), release.CreateReleaseCommand{Title: "  ", Type: release.TypeAlbum})
	requireCode(t, err, connect.CodeInvalidArgument, "title is required")
	for _, locale := range []string{"EN", "en-US", " ko ", "unsupported", " "} {
		t.Run(locale, func(t *testing.T) {
			_, err := h.service.CreateRelease(h.ctx(t), release.CreateReleaseCommand{Title: "Requested locale validation", SourceLocale: locale, Type: release.TypeAlbum})
			requireCode(t, err, connect.CodeInvalidArgument, "source_locale: must be a supported canonical locale")
		})
	}
	h.store.Routes["taken"] = true
	_, err = h.service.CreateRelease(h.ctx(t), release.CreateReleaseCommand{Title: "Routed", Type: release.TypeAlbum, Slug: text("taken")})
	requireCode(t, err, connect.CodeAlreadyExists, "")
	require.Empty(t, h.store.Releases())
	require.Empty(t, h.checker.Applied())
}

// The source locale is the explicit one, else the Member's preference, else
// Accept-Language, else the translation default.
func TestCreateReleasePicksItsSourceLocale(t *testing.T) {
	tests := []struct {
		name, explicit, preferred, acceptLanguage, fallback, want string
		preferenceErr, defaultErr                                 error
	}{
		{name: "explicit wins", explicit: "en", preferred: "ko", acceptLanguage: "ja", want: "en"},
		{name: "member preference", preferred: "ko", acceptLanguage: "ja", want: "ko"},
		{name: "accept language", acceptLanguage: "ja", fallback: "fr", want: "ja"},
		{name: "translation default", fallback: "fr", want: "fr"},
		{name: "failed preference falls through", preferred: "ko", preferenceErr: errStoreDown, acceptLanguage: "ja", want: "ja"},
		{name: "built-in default", defaultErr: errStoreDown, want: "en"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			if test.preferred != "" {
				h.store.Members[h.principal.memberID] = releasetest.Member{Nickname: "Editor", PreferredLocale: text(test.preferred)}
			}
			if test.preferenceErr != nil {
				h.store.Fail["MemberPreferredLocale"] = test.preferenceErr
			}
			h.store.SetTranslationDefault(test.fallback, test.defaultErr)
			created, err := h.service.CreateRelease(h.ctx(t), release.CreateReleaseCommand{
				Title: "Locale", Type: release.TypeAlbum, SourceLocale: test.explicit, AcceptLanguage: test.acceptLanguage,
			})
			require.NoError(t, err)
			require.Equal(t, test.want, created.Release.SourceLocale)
			_, _, ok := h.store.Locale(created.Release.ID, test.want)
			require.True(t, ok)
		})
	}
}

// Creation rechecks the administrator inside its transaction, before any
// initial state is written.
func TestCreateReleaseRechecksCurrentAdminBeforeInitialState(t *testing.T) {
	createCan, err := policyv1.Release.Create()
	require.NoError(t, err)
	h := newHarness(t, withChecker(authztest.AllowAll().Deny(createCan, nil)))
	_, err = h.service.CreateRelease(h.ctx(t), release.CreateReleaseCommand{Title: "Denied", Type: release.TypeAlbum})
	requireCode(t, err, connect.CodePermissionDenied, "admin access required")
	require.Equal(t, []mutation.Step{mutation.StepAuthorizationLock, mutation.StepLock, mutation.StepAccess, mutation.StepRollback}, h.units.Steps())
	require.Empty(t, h.store.Releases())
}

func TestReleaseMutationsAnswerDeniedOrInactiveCallersAsNotFound(t *testing.T) {
	for name, configure := range map[string]func(h *harness, releaseID string){
		"denied":   func(h *harness, releaseID string) { h.checker.Deny(can(t, policyv1.Release.Edit, releaseID), nil) },
		"inactive": func(h *harness, _ string) { h.fence.Deactivate(h.principal.identityID) },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			value := h.seedRelease(t, "Guarded")
			configure(h, value.ID)
			_, err := h.service.UpdateRelease(h.ctx(t), release.UpdateReleaseCommand{ID: value.ID, CatalogNumber: text("CAT")})
			requireCode(t, err, connect.CodeNotFound, "release not found: "+value.ID)
			stored, _ := h.store.Release(value.ID)
			require.Nil(t, stored.CatalogNumber)
			require.Equal(t, []mutation.Step{mutation.StepLock, mutation.StepAccess, mutation.StepRollback}, h.units.Steps())
		})
	}
}

// A response carries the times the Release stores, so a later read agrees.
func TestReleaseMutationResponsesMatchStoredTimes(t *testing.T) {
	h := newHarness(t)
	value := h.seedRelease(t, "Timed")
	updated, err := h.service.UpdateRelease(h.ctx(t), release.UpdateReleaseCommand{ID: value.ID, CatalogNumber: text("timestamp-catalog")})
	require.NoError(t, err)
	stored, _ := h.store.Release(value.ID)
	require.True(t, stored.UpdatedAt.Equal(updated.UpdatedAt))
	require.Equal(t, updated.UpdatedAt.Truncate(time.Microsecond), updated.UpdatedAt)

	published, err := h.service.PublishRelease(h.ctx(t), release.PublishReleaseCommand{ID: value.ID})
	require.NoError(t, err)
	stored, _ = h.store.Release(value.ID)
	require.True(t, stored.UpdatedAt.Equal(published.UpdatedAt))
	require.True(t, stored.PublishedAt.Equal(*published.PublishedAt))

	unpublished, err := h.service.UnpublishRelease(h.ctx(t), release.UnpublishReleaseCommand{ID: value.ID})
	require.NoError(t, err)
	stored, _ = h.store.Release(value.ID)
	require.True(t, stored.UpdatedAt.Equal(unpublished.UpdatedAt))
	require.Equal(t, release.StatusDraft, stored.Status)
}

func TestPublishingSignalsOnlyARealTransition(t *testing.T) {
	h := newHarness(t)
	value := h.seedRelease(t, "Published", func(r *release.Release) { r.Status = release.StatusPublished })
	change, err := h.service.PublishRelease(h.ctx(t), release.PublishReleaseCommand{ID: value.ID})
	require.NoError(t, err)
	require.False(t, change.Changed)
	require.Empty(t, h.audit.Records())
	require.Empty(t, h.store.Updates())

	change, err = h.service.UnpublishRelease(h.ctx(t), release.UnpublishReleaseCommand{ID: value.ID})
	require.NoError(t, err)
	require.True(t, change.Changed)
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditReleaseUpdated}, auditActions(h.audit))
	require.Equal(t, []release.ChangedField{{Path: "state.status", Kind: release.FieldState}}, h.store.Updates()[0].Fields)

	_, err = h.service.PublishRelease(h.ctx(t), release.PublishReleaseCommand{ID: value.ID})
	require.NoError(t, err)
	require.Equal(t, []release.ChangedField{
		{Path: "state.status", Kind: release.FieldState}, {Path: "state.published_at", Kind: release.FieldState},
	}, h.store.Updates()[1].Fields)
}

func TestUpdateReleaseSettings(t *testing.T) {
	t.Run("an unchanged update records and signals nothing", func(t *testing.T) {
		h := newHarness(t)
		value := h.seedRelease(t, "Same", func(r *release.Release) { r.CatalogNumber = text("CAT") })
		update, err := h.service.UpdateRelease(h.ctx(t), release.UpdateReleaseCommand{ID: value.ID, CatalogNumber: text("CAT")})
		require.NoError(t, err)
		require.False(t, update.Changed)
		require.Empty(t, h.audit.Records())
		require.Empty(t, h.store.Updates())
	})
	t.Run("a cleared date is a state change", func(t *testing.T) {
		h := newHarness(t)
		date := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
		value := h.seedRelease(t, "Dated", func(r *release.Release) { r.ReleaseDate = &date })
		update, err := h.service.UpdateRelease(h.ctx(t), release.UpdateReleaseCommand{ID: value.ID, ReleaseDateChange: release.ReleaseDateCleared})
		require.NoError(t, err)
		require.True(t, update.Changed)
		require.Nil(t, update.ReleaseDate)
		require.Equal(t, []string{"date"}, h.audit.Records()[0].ChangedFields)
		require.Equal(t, []release.ChangedField{{Path: "state.release_date", Kind: release.FieldState}}, h.store.Updates()[0].Fields)
	})
	t.Run("a slug another release holds is taken", func(t *testing.T) {
		h := newHarness(t)
		h.seedRelease(t, "Holder", func(r *release.Release) { r.Slug = text("held") })
		value := h.seedRelease(t, "Other")
		_, err := h.service.UpdateRelease(h.ctx(t), release.UpdateReleaseCommand{ID: value.ID, Slug: text("held")})
		requireCode(t, err, connect.CodeAlreadyExists, "release with slug \"held\" already exists")
		require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	})
	t.Run("a missing date is rejected", func(t *testing.T) {
		h := newHarness(t)
		value := h.seedRelease(t, "Dated")
		_, err := h.service.UpdateRelease(h.ctx(t), release.UpdateReleaseCommand{ID: value.ID, ReleaseDateChange: release.ReleaseDateSet})
		requireCode(t, err, connect.CodeInvalidArgument, "release_date: must be a valid timestamp")
	})
	t.Run("audit failure rolls the change back", func(t *testing.T) {
		auditDown := errors.New("audit down")
		h := newHarness(t, withAudit(&audittest.Recorder{Err: auditDown}))
		value := h.seedRelease(t, "Audited")
		_, err := h.service.UpdateRelease(h.ctx(t), release.UpdateReleaseCommand{ID: value.ID, CatalogNumber: text("CAT")})
		require.ErrorIs(t, err, auditDown)
		stored, _ := h.store.Release(value.ID)
		require.Nil(t, stored.CatalogNumber)
		require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	})
}

func TestCheckReleaseSlugAvailable(t *testing.T) {
	h := newHarness(t)
	holder := h.seedRelease(t, "Holder", func(r *release.Release) { r.Slug = text("held") })
	h.store.Routes["routed"] = true
	ctx := h.ctx(t)
	available, err := h.service.CheckReleaseSlugAvailable(ctx, release.CheckReleaseSlugQuery{Slug: "held"})
	require.NoError(t, err)
	require.False(t, available)
	available, err = h.service.CheckReleaseSlugAvailable(ctx, release.CheckReleaseSlugQuery{Slug: "held", ExcludeReleaseID: &holder.ID})
	require.NoError(t, err)
	require.True(t, available)
	available, err = h.service.CheckReleaseSlugAvailable(ctx, release.CheckReleaseSlugQuery{Slug: "routed"})
	require.NoError(t, err)
	require.False(t, available)
}

// deleteFixture is a Release with tracks, artwork and its share links.
type deleteFixture struct {
	*harness
	value  release.Release
	tracks []release.Track
}

func newDeleteFixture(t *testing.T, trackCount int) deleteFixture {
	t.Helper()
	h := newHarness(t)
	value := h.seedRelease(t, "Deleted")
	h.store.PutArtwork(value.ID, uuid.NewString())
	tracks := make([]release.Track, trackCount)
	for i := range tracks {
		tracks[i] = h.seedTrack(t, value.ID, i+1)
	}
	return deleteFixture{harness: h, value: value, tracks: tracks}
}

func TestDeleteReleaseCascadesWhatItOwns(t *testing.T) {
	f := newDeleteFixture(t, 2)
	require.NoError(t, f.service.DeleteRelease(f.ctx(t), release.DeleteReleaseCommand{ID: f.value.ID}))

	_, exists := f.store.Release(f.value.ID)
	require.False(t, exists)
	for _, track := range f.tracks {
		_, exists := f.store.Track(track.ID)
		require.False(t, exists)
	}
	_, documentExists := f.store.Document(uuid.MustParse(*f.value.ContentDocumentID))
	require.False(t, documentExists)
	require.Equal(t, 1, f.store.ShareLinkDeletions(f.value.ID))
	require.Equal(t, []string{f.value.ID}, f.store.OGCancels())
	require.Empty(t, f.uploads.cleanedTracks(), "a Release without uploads cleans up nothing")
	require.Len(t, f.jobs.waveforms, 1)
	require.ElementsMatch(t, []string{f.tracks[0].ID, f.tracks[1].ID}, f.jobs.waveforms[0])
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditReleaseDeleted}, auditActions(f.audit))
	applied := f.checker.Applied()
	require.Len(t, applied, 1)
	require.Len(t, applied[0], 3, "the Release policy and each Track policy go in one batch")
	require.Empty(t, f.checker.Compensated())
}

func TestDeleteReleaseAnswersUnknownReleaseAsNotFound(t *testing.T) {
	h := newHarness(t)
	unknown := uuid.NewString()
	err := h.service.DeleteRelease(h.ctx(t), release.DeleteReleaseCommand{ID: unknown})
	requireCode(t, err, connect.CodeNotFound, "release not found: "+unknown)
	require.Empty(t, h.uploads.cleanedTracks())
}

// A cascade too large for one atomic authorization batch fails before
// upload cleanup or any write.
func TestDeleteReleaseRejectsOversizedAuthorizationBatchBeforeMutation(t *testing.T) {
	f := newDeleteFixture(t, 1000)
	err := f.service.DeleteRelease(f.ctx(t), release.DeleteReleaseCommand{ID: f.value.ID})
	requireCode(t, err, connect.CodeFailedPrecondition, "release has too many tracks to delete atomically; delete tracks individually until at most 999 remain")
	require.Empty(t, f.uploads.cleanedTracks())
	_, exists := f.store.Release(f.value.ID)
	require.True(t, exists)
	require.Empty(t, f.checker.Applied())
}

func TestDeleteReleaseStopsWhenAnUploadIsFinalizing(t *testing.T) {
	f := newDeleteFixture(t, 1)
	f.uploads.cleanup = func(string) error { return release.ErrTrackUploadSessionNotAbortable }
	f.store.SetUploadSession(f.tracks[0].ID, true)
	err := f.service.DeleteRelease(f.ctx(t), release.DeleteReleaseCommand{ID: f.value.ID})
	requireCode(t, err, connect.CodeFailedPrecondition, "track audio upload is finalizing; retry its completion before deleting the release")
	_, exists := f.store.Release(f.value.ID)
	require.True(t, exists)
}

// An upload that starts again between cleanup and each deletion attempt
// exhausts the retry budget; the deletion writes nothing.
func TestDeleteReleaseGivesUpWhenUploadsKeepStarting(t *testing.T) {
	f := newDeleteFixture(t, 1)
	trackID := f.tracks[0].ID
	f.store.SetUploadSession(trackID, true)
	f.uploads.cleanup = func(string) error {
		f.store.SetUploadSession(trackID, false)
		return nil
	}
	checks := 0
	// Odd checks are the deletion transaction's: an upload has started by
	// then, and the transaction's rollback forgets it again.
	f.store.OnUploadCheck = func() {
		checks++
		if checks%2 == 1 {
			f.store.SetUploadSession(trackID, true)
		}
	}
	err := f.service.DeleteRelease(f.ctx(t), release.DeleteReleaseCommand{ID: f.value.ID})
	requireCode(t, err, connect.CodeFailedPrecondition, "track audio uploads changed during deletion; retry the request")
	require.Equal(t, 2*release.MaxTrackUploadCleanupPasses+1, checks)
	_, exists := f.store.Release(f.value.ID)
	require.True(t, exists)
	require.Empty(t, f.checker.Applied())
}

// Cleanup that cannot clear a Track's uploads fails the deletion.
func TestDeleteReleaseFailsWhenCleanupLeavesUploads(t *testing.T) {
	f := newDeleteFixture(t, 1)
	f.store.SetUploadSession(f.tracks[0].ID, true)
	err := f.service.DeleteRelease(f.ctx(t), release.DeleteReleaseCommand{ID: f.value.ID})
	require.ErrorIs(t, err, release.ErrTrackUploadSessionsChanged)
	requireCode(t, err, connect.CodeInternal, "")
	require.Len(t, f.uploads.cleanedTracks(), release.MaxTrackUploadCleanupPasses)
	_, exists := f.store.Release(f.value.ID)
	require.True(t, exists)
}

// The caller's delete authority is decided under the Release lock before
// cleanup aborts any upload.
func TestDeleteReleaseChecksAuthorityBeforeCleanup(t *testing.T) {
	f := newDeleteFixture(t, 1)
	f.checker.Deny(can(t, policyv1.Release.Delete, f.value.ID), nil)
	err := f.service.DeleteRelease(f.ctx(t), release.DeleteReleaseCommand{ID: f.value.ID})
	requireCode(t, err, connect.CodeNotFound, "release not found: "+f.value.ID)
	require.Empty(t, f.uploads.cleanedTracks())
}

func TestDeleteReleaseLogsWaveformCancelFailureAfterCommit(t *testing.T) {
	f := newDeleteFixture(t, 1)
	f.jobs.err = errors.New("waveform queue down")
	require.NoError(t, f.service.DeleteRelease(f.ctx(t), release.DeleteReleaseCommand{ID: f.value.ID}))
	_, exists := f.store.Release(f.value.ID)
	require.False(t, exists)
}

func TestListAndGetReleaseProjectViews(t *testing.T) {
	h := newHarness(t)
	value := h.seedRelease(t, "Listed")
	h.store.PutArtwork(value.ID, "artwork-file")
	h.store.ReadyAssets["artwork-file"] = nil
	h.seedTrack(t, value.ID, 1)
	h.store.AddReleaseCredit(value.ID, release.ReleaseCredit{ID: uuid.NewString(), CreditedName: text("Credit")})
	page, err := h.service.ListReleasesAdmin(h.ctx(t), release.ListReleasesQuery{})
	require.NoError(t, err)
	require.Equal(t, int32(20), page.Limit)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, "Listed", page.Releases[0].View.Title)
	require.Equal(t, int32(1), page.Releases[0].TrackCount)
	require.Equal(t, int32(1), page.Releases[0].CreditCount)

	view, err := h.service.GetRelease(h.ctx(t), release.GetReleaseQuery{ID: value.ID})
	require.NoError(t, err)
	require.Equal(t, "Listed", view.Title)
	require.Equal(t, h.revision(t, value.ID).String(), view.Revision)

	listCan, err := policyv1.Release.List()
	require.NoError(t, err)
	denied := newHarness(t, withChecker(authztest.AllowAll().Deny(listCan, nil)))
	_, err = denied.service.ListReleasesAdmin(denied.ctx(t), release.ListReleasesQuery{})
	requireCode(t, err, connect.CodePermissionDenied, "admin access required")
	_, err = h.service.GetRelease(context.Background(), release.GetReleaseQuery{ID: value.ID})
	requireCode(t, err, connect.CodeUnauthenticated, "authentication required")
}
