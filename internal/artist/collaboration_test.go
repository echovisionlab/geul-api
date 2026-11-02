package artist_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/artist/artisttest"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func set(value string) *entityparticipant.StringMutation {
	return &entityparticipant.StringMutation{Set: &value}
}

// roomFixture is an Artist with a source title and a contributor of its
// collaboration room.
type roomFixture struct {
	h           *harness
	artist      artist.Artist
	contributor principal
}

func newRoomFixture(t *testing.T) roomFixture {
	t.Helper()
	h := newHarness(t)
	return roomFixture{h: h, artist: h.seedArtist(t, "Room"), contributor: h.member(newPrincipal())}
}

func (f roomFixture) metadata(t *testing.T, locale string, update *artist.DocumentMetadataUpdate, expected string) (artist.MetadataUpdate, error) {
	t.Helper()
	return f.h.service.UpdateArtistDocumentMetadata(f.contributor.context(t), artist.UpdateArtistDocumentMetadataCommand{
		ArtistID: f.artist.ID, Locale: locale, ExpectedRevision: expected, Update: update,
		ContributorMemberIDs: []string{f.contributor.memberID},
	})
}

func TestUpdateArtistDocumentMetadataSavesTheArtistSpecificFields(t *testing.T) {
	f := newRoomFixture(t)
	first, second := uuid.NewString(), uuid.NewString()
	f.h.store.PutLabel(first, "First", false)
	f.h.store.PutLabel(second, "Second", true)
	before := f.h.revision(t, f.artist.ID)

	saved, err := f.metadata(t, "en", &artist.DocumentMetadataUpdate{
		RealName: set(" Real Name "), SocialLinksPresent: true, SocialLinks: map[string]string{"site": "https://artist.example"},
		LabelIDsPresent: true, LabelIDs: []string{second, first},
	}, before.String())

	require.NoError(t, err)
	require.Equal(t, "en", saved.Locale)
	require.True(t, saved.Advance.Changed)
	require.NotEqual(t, before, saved.Advance.DocumentRevision)
	stored, _ := f.h.store.Artist(f.artist.ID)
	require.Equal(t, "Real Name", *stored.RealName)
	require.Equal(t, map[string]string{"site": "https://artist.example"}, stored.SocialLinks)
	labels := []string{first, second}
	if second < first {
		labels = []string{second, first}
	}
	require.Equal(t, labels, f.h.store.LinkedLabels(f.artist.ID), "Labels are stored in ID order")
	require.Empty(t, f.h.audit.Records(), "document-metadata saves have never been audited")
	require.Empty(t, f.h.checker.Applied(), "only a parent change writes relationships")

	again, err := f.metadata(t, "en", &artist.DocumentMetadataUpdate{
		RealName: set("Real Name"), LabelIDsPresent: true, LabelIDs: []string{first, second},
	}, saved.Advance.DocumentRevision.String())
	require.NoError(t, err)
	require.False(t, again.Advance.Changed, "current values are a no-op")

	cleared, err := f.metadata(t, "en", &artist.DocumentMetadataUpdate{LabelIDsPresent: true}, again.Advance.DocumentRevision.String())
	require.NoError(t, err)
	require.True(t, cleared.Advance.Changed)
	require.Empty(t, f.h.store.LinkedLabels(f.artist.ID), "present but empty Labels clear them")
}

func TestUpdateArtistDocumentMetadataMovesTheParentRelationship(t *testing.T) {
	f := newRoomFixture(t)
	parent := f.h.seedArtist(t, "Parent")

	saved, err := f.metadata(t, "en", &artist.DocumentMetadataUpdate{ParentArtistID: set(parent.ID)}, f.h.revision(t, f.artist.ID).String())

	require.NoError(t, err)
	require.True(t, saved.Advance.Changed)
	stored, _ := f.h.store.Artist(f.artist.ID)
	require.Equal(t, parent.ID, *stored.ParentArtistID)
	touch, err := policyv1.Artist.TouchParent(f.artist.ID, parent.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch}}, f.h.checker.Applied())
	require.Equal(t, 2, f.h.store.ParentGraphLocks(), "a reparent takes the hierarchy lock before the content fence and validates under it")
}

func TestUpdateArtistDocumentMetadataRejections(t *testing.T) {
	missingLabel := uuid.NewString()
	duplicate := uuid.NewString()
	for _, test := range []struct {
		name    string
		locale  string
		update  *artist.DocumentMetadataUpdate
		stale   bool
		code    connect.Code
		message string
	}{
		{name: "a target-locale room", locale: "ko", update: &artist.DocumentMetadataUpdate{RealName: set("Name")},
			code: connect.CodeInvalidArgument, message: "Artist document metadata and audit checkpoints are source-locale only"},
		{name: "a non-canonical locale", locale: "EN", update: &artist.DocumentMetadataUpdate{RealName: set("Name")},
			code: connect.CodeInvalidArgument},
		{name: "empty update", locale: "en", update: &artist.DocumentMetadataUpdate{},
			code: connect.CodeInvalidArgument, message: "update: at least one metadata value is required"},
		{name: "an unknown Label", locale: "en", update: &artist.DocumentMetadataUpdate{LabelIDsPresent: true, LabelIDs: []string{missingLabel}},
			code: connect.CodeInvalidArgument, message: "label_ids: contains a Label that does not exist"},
		{name: "a duplicate Label", locale: "en", update: &artist.DocumentMetadataUpdate{LabelIDsPresent: true, LabelIDs: []string{duplicate, " " + duplicate}},
			code: connect.CodeInvalidArgument, message: "label_ids: must contain unique Label IDs"},
		{name: "an invalid Label ID", locale: "en", update: &artist.DocumentMetadataUpdate{LabelIDsPresent: true, LabelIDs: []string{"label"}},
			code: connect.CodeInvalidArgument, message: "label_ids: must contain valid Label IDs"},
		{name: "a stale revision", locale: "en", update: &artist.DocumentMetadataUpdate{RealName: set("Name")}, stale: true,
			code: connect.CodeFailedPrecondition, message: "artist content revision changed; reload before saving"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newRoomFixture(t)
			expected := f.h.revision(t, f.artist.ID).String()
			if test.stale {
				expected = uuid.NewString()
			}

			_, err := f.metadata(t, test.locale, test.update, expected)

			requireCode(t, err, test.code, test.message)
			stored, _ := f.h.store.Artist(f.artist.ID)
			require.Nil(t, stored.RealName)
			require.Empty(t, f.h.store.LinkedLabels(f.artist.ID))
		})
	}
}

func TestUpdateArtistLocaleMetadataSavesTheSourceTitleWithoutAnAudit(t *testing.T) {
	f := newRoomFixture(t)
	revision := f.h.revision(t, f.artist.ID)
	title := "Renamed"

	saved, err := f.h.service.UpdateArtistLocaleMetadata(f.contributor.context(t), artist.UpdateArtistLocaleMetadataCommand{
		ArtistID: f.artist.ID, Locale: "en", Title: &title, ExpectedRevision: revision.String(),
		ContributorMemberIDs: []string{f.contributor.memberID},
	})

	require.NoError(t, err)
	require.True(t, saved.Result.Changed)
	stored, ok := f.h.store.Locale(f.artist.ID, "en")
	require.True(t, ok)
	require.Equal(t, "Renamed", *stored.Title)
	require.Empty(t, f.h.audit.Records(), "collaboration title saves have never been audited")
	require.Equal(t, []artisttest.OGRequest{{ArtistID: f.artist.ID, Reason: "artist_source_title_saved"}}, f.h.store.OGRequests())
	updates := f.h.store.Updates()
	require.Len(t, updates, 1)
	require.Equal(t, artist.LocaleUpdate, updates[0].Kind)
	require.Equal(t, []string{"title"}, updates[0].Locale.Fields)
	require.True(t, updates[0].Locale.DocumentStateChanged)
}

func sourceParagraphBatch(revision uuid.UUID, contributor, locale string, blockID uuid.UUID, text string) *contentv1.RichTextBlockMutationBatch {
	return &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT,
		ExpectedRevision:        revision.String(), ContributorMemberIds: []string{contributor},
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: locale,
			Mutations: []*contentv1.RichTextBlockLocaleMutation{{Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{
				Upsert: &contentv1.UpsertRichTextBlockLocale{Block: &contentv1.RichTextBlockLocale{
					BlockId: blockID.String(),
					Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
						Props:   &contentv1.ParagraphLocaleProps{},
						Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: text}}}},
					}},
				}},
			}}},
		}},
	}
}

func TestApplyArtistBlockBatchStampsTheSourceAndRequestsItsOG(t *testing.T) {
	f := newRoomFixture(t)
	revision := f.h.revision(t, f.artist.ID)

	result, err := f.h.service.ApplyArtistBlockBatch(f.contributor.context(t), artist.ApplyArtistBlockBatchCommand{
		ArtistID: f.artist.ID, Locale: "en",
		Batch: sourceParagraphBatch(revision, f.contributor.memberID, "en", uuid.New(), "source"),
	})

	require.NoError(t, err)
	require.True(t, result.Content.Changed)
	require.NotEqual(t, revision, result.Content.DocumentRevision)
	stored, _ := f.h.store.Artist(f.artist.ID)
	require.NotNil(t, stored.UpdatedAt, "a source save stamps the Artist")
	require.Equal(t, []artisttest.OGRequest{{ArtistID: f.artist.ID, Reason: "artist_source_document_saved"}}, f.h.store.OGRequests())
	require.Len(t, f.h.audit.Records(), 1)
	updates := f.h.store.Updates()
	require.Len(t, updates, 1)
	require.Equal(t, artist.SourceCollaboration, updates[0].Locale.Source)
	require.Equal(t, []string{f.contributor.memberID}, updates[0].Locale.Contributors)
}

func TestApplyArtistBlockBatchRejectsARevokedRoomAndAStrangerContributor(t *testing.T) {
	f := newRoomFixture(t)
	revision := f.h.revision(t, f.artist.ID)
	f.h.store.RevokeRoom(f.artist.ID)

	_, err := f.h.service.ApplyArtistBlockBatch(f.contributor.context(t), artist.ApplyArtistBlockBatchCommand{
		ArtistID: f.artist.ID, Locale: "en",
		Batch: sourceParagraphBatch(revision, f.contributor.memberID, "en", uuid.New(), "revoked"),
	})
	requireCode(t, err, connect.CodePermissionDenied, "")

	_, err = f.h.service.ApplyArtistBlockBatch(f.contributor.context(t), artist.ApplyArtistBlockBatchCommand{
		ArtistID: f.artist.ID, Locale: "en",
		Batch: sourceParagraphBatch(revision, uuid.NewString(), "en", uuid.New(), "stranger"),
	})
	requireCode(t, err, connect.CodeInvalidArgument, "contributor_member_ids: contains a Member that does not exist")
	require.Equal(t, revision, f.h.revision(t, f.artist.ID))
	require.Empty(t, f.h.store.OGRequests())
}

func TestLoadArtistBlockDocumentShowsTheRoomOfTheSessionPrincipal(t *testing.T) {
	f := newRoomFixture(t)
	labelID := uuid.NewString()
	f.h.store.PutLabel(labelID, "Label", false)
	f.h.store.LinkLabels(f.artist.ID, labelID)
	target := "目標"
	f.h.store.PutLocale(f.artist.ID, artist.LocaleTitle{Locale: "ja", Title: &target, UpdatedAt: time.Now().UTC()})
	sessionID := uuid.NewString()
	f.h.store.PutSession(sessionID, f.contributor.user())

	room, err := f.h.service.LoadArtistBlockDocument(t.Context(), artist.LoadArtistBlockDocumentQuery{
		ArtistID: f.artist.ID, Locale: "ja", SessionID: sessionID,
	})

	require.NoError(t, err)
	require.Equal(t, "ja", room.Locale)
	require.True(t, room.LocaleExists)
	require.NotNil(t, room.TargetRevision)
	require.Equal(t, "目標", *room.LocaleMetadata.Title)
	require.Equal(t, "Room", *room.SourceMetadata.Title)
	require.Equal(t, []string{labelID}, room.Metadata.LabelIDs)
	require.Equal(t, f.h.revision(t, f.artist.ID).String(), room.DocumentRevision)

	_, err = f.h.service.LoadArtistBlockDocument(t.Context(), artist.LoadArtistBlockDocumentQuery{
		ArtistID: f.artist.ID, Locale: "ja", SessionID: uuid.NewString(),
	})
	requireCode(t, err, connect.CodeUnauthenticated, "")
	banned := f.contributor.user()
	banned.Banned = true
	f.h.store.PutSession(sessionID, banned)
	_, err = f.h.service.LoadArtistBlockDocument(t.Context(), artist.LoadArtistBlockDocumentQuery{
		ArtistID: f.artist.ID, Locale: "ja", SessionID: sessionID,
	})
	requireCode(t, err, connect.CodePermissionDenied, "account is banned")
}

func TestApplyTypedTranslationCandidateRequiresTheLockedSourceRevision(t *testing.T) {
	f := newRoomFixture(t)
	title := "Traduit"
	candidate := &translation.Candidate{
		Title: &title, ContentDocumentRevision: uuid.NewString(),
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "fr"},
	}
	job := artist.TranslationJob{EntityType: "artist", EntityID: f.artist.ID, TargetLocale: "fr", RequestedByMemberID: f.contributor.memberID}

	err := artist.ApplyTypedTranslationCandidate(t.Context(), artist.Joined{Store: f.h.store, Audit: f.h.audit}, job, candidate, translation.EntryWrite{Now: time.Now().UTC()})

	requireCode(t, err, connect.CodeInvalidArgument, "batch: Artist target document identity and revision must match the locked state")
	_, exists := f.h.store.Locale(f.artist.ID, "fr")
	require.False(t, exists)
	require.Empty(t, f.h.audit.Records())

	candidate.ContentDocumentRevision = f.h.revision(t, f.artist.ID).String()
	require.NoError(t, artist.ApplyTypedTranslationCandidate(t.Context(), artist.Joined{Store: f.h.store, Audit: f.h.audit}, job, candidate, translation.EntryWrite{Now: time.Now().UTC()}))
	stored, exists := f.h.store.Locale(f.artist.ID, "fr")
	require.True(t, exists)
	require.Equal(t, "Traduit", *stored.Title)
	require.Len(t, f.h.audit.Records(), 1)
	require.Equal(t, f.h.revision(t, f.artist.ID), uuid.MustParse(candidate.ContentDocumentRevision), "a target delivery keeps the shared revision")
}

func TestUpdateArtistDocumentMetadataRejectsAHierarchyCycle(t *testing.T) {
	f := newRoomFixture(t)
	child := f.h.seedArtist(t, "Child", func(value *artist.Artist) { value.ParentArtistID = &f.artist.ID })
	grandchild := f.h.seedArtist(t, "Grandchild", func(value *artist.Artist) { value.ParentArtistID = &child.ID })

	_, err := f.metadata(t, "en", &artist.DocumentMetadataUpdate{ParentArtistID: set(grandchild.ID)}, f.h.revision(t, f.artist.ID).String())
	requireCode(t, err, connect.CodeInvalidArgument, "parent_artist_id: would create an artist hierarchy cycle")
	_, err = f.metadata(t, "en", &artist.DocumentMetadataUpdate{ParentArtistID: set(f.artist.ID)}, f.h.revision(t, f.artist.ID).String())
	requireCode(t, err, connect.CodeInvalidArgument, "parent_artist_id: cannot reference the same artist")
	stored, _ := f.h.store.Artist(f.artist.ID)
	require.Nil(t, stored.ParentArtistID)
	require.Empty(t, f.h.checker.Applied())
}

func TestCheckArtistSlugAvailableExcludesTheArtistItself(t *testing.T) {
	h := newHarness(t)
	caller := h.member(newPrincipal())
	value := h.seedArtist(t, "Slugged", func(value *artist.Artist) { value.Slug = stringPointer("slugged") })
	h.store.PutRoute("routed")

	for _, test := range []struct {
		slug      string
		exclude   *string
		available bool
	}{
		{slug: "slugged"},
		{slug: "slugged", exclude: &value.ID, available: true},
		{slug: "routed"},
		{slug: "free", available: true},
	} {
		available, err := h.service.CheckArtistSlugAvailable(caller.context(t), artist.CheckArtistSlugAvailableQuery{Slug: test.slug, ExcludeArtistID: test.exclude})
		require.NoError(t, err)
		require.Equal(t, test.available, available, test.slug)
	}
}
