package release_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/release/releasetest"
	"github.com/echovisionlab/geul-api/internal/translation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Every relation setter needs the snapshot the client observed.
func TestReleaseRelationSettersRequireTheObservedSnapshot(t *testing.T) {
	h := newHarness(t)
	value := h.seedRelease(t, "Album")
	ctx := h.ctx(t)
	for name, set := range map[string]func() error{
		"artists":    func() error { return h.service.SetReleaseArtists(ctx, release.SetReleaseArtistsCommand{ReleaseID: value.ID}) },
		"labels":     func() error { return h.service.SetReleaseLabels(ctx, release.SetReleaseLabelsCommand{ReleaseID: value.ID}) },
		"formats":    func() error { return h.service.SetReleaseFormats(ctx, release.SetReleaseFormatsCommand{ReleaseID: value.ID}) },
		"credits":    func() error { return h.service.SetReleaseCredits(ctx, release.SetReleaseCreditsCommand{ReleaseID: value.ID}) },
		"categories": func() error { return h.service.SetReleaseCategories(ctx, release.SetReleaseCategoriesCommand{ReleaseID: value.ID}) },
		"genres":     func() error { return h.service.SetReleaseGenres(ctx, release.SetReleaseGenresCommand{ReleaseID: value.ID}) },
		"styles":     func() error { return h.service.SetReleaseStyles(ctx, release.SetReleaseStylesCommand{ReleaseID: value.ID}) },
	} {
		t.Run(name, func(t *testing.T) {
			requireCode(t, set(), connect.CodeInvalidArgument, "observed: is required")
		})
	}
	require.Empty(t, h.audit.Records())
}

// A reference set edit removes only what the client observed and keeps
// what a peer added meanwhile; an unchanged edit records nothing.
func TestSetReleaseCategoriesMergesAgainstTheObservation(t *testing.T) {
	h := newHarness(t)
	value := h.seedRelease(t, "Album")
	observed, peer, added := uuid.NewString(), uuid.NewString(), uuid.NewString()
	h.store.AddReference(release.ReferenceCategories, value.ID, observed)
	h.store.AddReference(release.ReferenceCategories, value.ID, peer)
	before, _ := h.store.Release(value.ID)
	require.NoError(t, h.service.SetReleaseCategories(h.ctx(t), release.SetReleaseCategoriesCommand{
		ReleaseID: value.ID, CategoryIDs: []string{added}, Observed: &release.Snapshot[string]{Items: []string{observed}},
	}))
	ids, err := h.store.ReferenceIDs(t.Context(), release.ReferenceCategories, value.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{peer, added}, ids)
	after, _ := h.store.Release(value.ID)
	require.True(t, after.UpdatedAt.After(before.UpdatedAt))
	require.Equal(t, []string{"categories"}, h.audit.Records()[0].ChangedFields)
	require.Equal(t, []release.ChangedField{{Path: "relations.categories", Kind: release.FieldRelation}}, h.store.Updates()[0].Fields)

	require.NoError(t, h.service.SetReleaseCategories(h.ctx(t), release.SetReleaseCategoriesCommand{
		ReleaseID: value.ID, CategoryIDs: []string{peer, added}, Observed: &release.Snapshot[string]{Items: []string{peer, added}},
	}))
	require.Len(t, h.audit.Records(), 1)
	require.Len(t, h.store.Updates(), 1)
}

func TestSetReleaseArtistsAndLabelsApplyTheOrderIntent(t *testing.T) {
	h := newHarness(t)
	value := h.seedRelease(t, "Album")
	first, second := uuid.NewString(), uuid.NewString()
	h.store.AddReleaseArtist(value.ID, release.ReleaseArtist{ArtistID: first, SortOrder: 0})
	h.store.AddReleaseArtist(value.ID, release.ReleaseArtist{ArtistID: second, SortOrder: 1})
	observed := []*release.ReleaseArtistInput{{ArtistID: first}, {ArtistID: second}}
	require.NoError(t, h.service.SetReleaseArtists(h.ctx(t), release.SetReleaseArtistsCommand{
		ReleaseID: value.ID, Artists: observed, Observed: &release.Snapshot[*release.ReleaseArtistInput]{Items: observed},
		OrderIntent: &release.RelationOrderIntent{ItemID: second, NextItemID: &first},
	}))
	artists, err := h.store.ReleaseArtists(t.Context(), value.ID)
	require.NoError(t, err)
	require.Equal(t, []release.ReleaseArtist{{ArtistID: second, SortOrder: 0}, {ArtistID: first, SortOrder: 1}}, artists)

	label := uuid.NewString()
	require.NoError(t, h.service.SetReleaseLabels(h.ctx(t), release.SetReleaseLabelsCommand{
		ReleaseID: value.ID, Labels: []*release.ReleaseLabelInput{{LabelID: label, CatalogNumber: text("CAT")}},
		Observed: &release.Snapshot[*release.ReleaseLabelInput]{},
	}))
	labels, err := h.store.ReleaseLabels(t.Context(), value.ID)
	require.NoError(t, err)
	require.Equal(t, []release.ReleaseLabel{{LabelID: label, CatalogNumber: text("CAT")}}, labels)
	err = h.service.SetReleaseLabels(h.ctx(t), release.SetReleaseLabelsCommand{
		ReleaseID: value.ID, Labels: []*release.ReleaseLabelInput{nil}, Observed: &release.Snapshot[*release.ReleaseLabelInput]{},
	})
	requireCode(t, err, connect.CodeInvalidArgument, "labels: must not contain null")
}

func TestSetReleaseCreditsLocksCreditedMembers(t *testing.T) {
	h := newHarness(t)
	value := h.seedRelease(t, "Album")
	member := uuid.NewString()
	h.store.Members[member] = releaseMember("Credited")
	require.NoError(t, h.service.SetReleaseCredits(h.ctx(t), release.SetReleaseCreditsCommand{
		ReleaseID: value.ID, Credits: []*release.CreditInput{{MemberID: &member, CreditRole: text("Mix")}},
		Observed: &release.Snapshot[*release.CreditInput]{},
	}))
	credits, err := h.store.ReleaseCredits(t.Context(), value.ID)
	require.NoError(t, err)
	require.Len(t, credits, 1)
	require.NotEmpty(t, credits[0].ID, "a new credit gets its ID before it is stored")
	require.Contains(t, h.store.LockedMembers(), member)

	missing := uuid.NewString()
	err = h.service.SetReleaseCredits(h.ctx(t), release.SetReleaseCreditsCommand{
		ReleaseID: value.ID, Credits: []*release.CreditInput{{MemberID: &missing}},
		Observed: &release.Snapshot[*release.CreditInput]{},
	})
	requireCode(t, err, connect.CodeInvalidArgument, "credit[0].member_id: not found: "+missing)
}

func TestGetReleaseRelationsReturnsTheEditorSnapshot(t *testing.T) {
	h := newHarness(t)
	value := h.seedRelease(t, "Album")
	artist, label, genre, format, member := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	h.store.Artists[artist] = release.Summary{Title: "Artist", Slug: "artist"}
	h.store.Labels[label] = release.Summary{Title: "Label"}
	h.store.Catalog[genre] = release.Reference{ID: genre, Name: "Genre", Slug: "genre"}
	h.store.Catalog[format] = release.Reference{ID: format, Name: "Vinyl", Slug: "vinyl"}
	h.store.Members[member] = releaseMember("Credited Member")
	h.store.AddReleaseArtist(value.ID, release.ReleaseArtist{ArtistID: artist})
	h.store.AddReleaseArtist(value.ID, release.ReleaseArtist{ArtistID: uuid.NewString(), SortOrder: 1})
	h.store.AddReleaseLabel(value.ID, release.ReleaseLabel{LabelID: label, CatalogNumber: text("CAT")})
	h.store.AddReference(release.ReferenceGenres, value.ID, genre)
	h.store.AddReleaseFormat(value.ID, release.ReleaseFormat{FormatID: format, FormatDescription: text("180g")})
	h.store.AddReleaseCredit(value.ID, release.ReleaseCredit{ID: uuid.NewString(), ArtistID: &artist})
	h.store.AddReleaseCredit(value.ID, release.ReleaseCredit{ID: uuid.NewString(), MemberID: &member, SortOrder: 1})

	relations, err := h.service.GetReleaseRelations(h.ctx(t), release.GetReleaseRelationsQuery{ReleaseID: value.ID})
	require.NoError(t, err)
	require.Equal(t, []release.EditorArtist{{ArtistID: artist, Name: "Artist", Slug: text("artist")}}, relations.Artists, "an Artist without a summary is left out")
	require.Equal(t, []release.EditorLabel{{LabelID: label, Name: "Label", CatalogNumber: text("CAT")}}, relations.Labels)
	require.Equal(t, []release.Reference{{ID: genre, Name: "Genre", Slug: "genre"}}, relations.Genres)
	require.Empty(t, relations.Categories)
	require.NotNil(t, relations.Categories)
	require.Equal(t, "180g", *relations.Formats[0].Description)
	require.Equal(t, "Artist", *relations.Credits[0].ArtistName)
	require.Equal(t, "Credited Member", *relations.Credits[1].MemberName)

	h.checker.Deny(can(t, policyv1.Release.View, value.ID), nil)
	_, err = h.service.GetReleaseRelations(h.ctx(t), release.GetReleaseRelationsQuery{ReleaseID: value.ID})
	requireCode(t, err, connect.CodeNotFound, "release not found: "+value.ID)
	missing := uuid.NewString()
	allowed := newHarness(t)
	_, err = allowed.service.GetReleaseRelations(allowed.ctx(t), release.GetReleaseRelationsQuery{ReleaseID: missing})
	requireCode(t, err, connect.CodeNotFound, "release not found: "+missing)
}

func TestReleaseArtworkReplacesAndUnlinksWithoutDeletingFiles(t *testing.T) {
	h := newHarness(t)
	value := h.seedRelease(t, "Album")
	requested, stored := "A1B2C3D4-0000-4000-8000-000000000001", "a1b2c3d4-0000-4000-8000-000000000001"
	h.store.PutFile(requested, stored)
	asset := &commonv1.AssetRef{AssetId: "artwork"}
	h.store.ReadyAssets[stored] = asset

	ref, err := h.service.SetReleaseArtwork(h.ctx(t), release.SetReleaseArtworkCommand{ReleaseID: value.ID, FileID: requested})
	require.NoError(t, err)
	require.Same(t, asset, ref)
	fileID, bound := h.store.Artwork(value.ID)
	require.Equal(t, requested, fileID)
	require.Equal(t, stored, bound, "the binding takes the File as the file row stores it")
	require.Equal(t, []string{value.ID}, h.store.OGCancels())
	require.Equal(t, sharedtelemetry.AuditCollectionOperationAdded, h.audit.Records()[0].CollectionOperation)
	require.Equal(t, []release.ChangedField{{Path: "media.artwork", Kind: release.FieldMedia}}, h.store.Updates()[0].Fields)

	ref, err = h.service.SetReleaseArtwork(h.ctx(t), release.SetReleaseArtworkCommand{ReleaseID: value.ID, FileID: requested})
	require.NoError(t, err)
	require.Nil(t, ref, "the current artwork changes nothing")
	require.Len(t, h.audit.Records(), 1)

	require.NoError(t, h.service.DeleteReleaseArtwork(h.ctx(t), release.DeleteReleaseArtworkCommand{ReleaseID: value.ID}))
	fileID, bound = h.store.Artwork(value.ID)
	require.Empty(t, fileID)
	require.Empty(t, bound)
	require.Equal(t, sharedtelemetry.AuditCollectionOperationRemoved, h.audit.Records()[1].CollectionOperation)
	require.NoError(t, h.service.DeleteReleaseArtwork(h.ctx(t), release.DeleteReleaseArtworkCommand{ReleaseID: value.ID}))
	require.Len(t, h.audit.Records(), 2, "removing an absent artwork records nothing")

	missing := uuid.NewString()
	_, err = h.service.SetReleaseArtwork(h.ctx(t), release.SetReleaseArtworkCommand{ReleaseID: value.ID, FileID: missing})
	requireCode(t, err, connect.CodeNotFound, "file not found: "+missing)
	unknown := uuid.NewString()
	_, err = h.service.SetReleaseArtwork(h.ctx(t), release.SetReleaseArtworkCommand{ReleaseID: unknown, FileID: requested})
	requireCode(t, err, connect.CodeNotFound, "release not found: "+unknown)
}

// Translation delivery is fenced by the Release root: a draft Release takes
// the result, a deleted one rejects it without writing.
func TestTranslationDeliveryFollowsTheReleaseRoot(t *testing.T) {
	h := newHarness(t)
	document := compactDocument("en", "Body")
	created, err := h.service.CreateRelease(h.ctx(t), release.CreateReleaseCommand{Title: "Album", Type: release.TypeAlbum, SourceLocale: "en", Document: document})
	require.NoError(t, err)
	value := created.Release
	blockID := document.Base.Nodes[0].Block.Id
	job := release.TranslationJob{
		EntityType: "release", EntityID: value.ID, SourceLocale: "en", TargetLocale: "ko", RequestedByMemberID: h.principal.memberID,
	}
	candidate := func(releaseID string) *translation.Candidate {
		return &translation.Candidate{
			ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko", Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: blockID, Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
					Props:   &contentv1.ParagraphLocaleProps{},
					Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "본문"}}}},
				}},
			}}},
			ContentDocumentRevision: h.revision(t, releaseID).String(), ReleaseCreditNotes: map[string]string{},
		}
	}
	before := len(h.audit.Records())
	joined := release.Joined{Store: h.store, Audit: h.audit}
	require.NoError(t, release.ApplyTypedTranslationCandidate(t.Context(), joined, job, candidate(value.ID), translation.EntryWrite{Now: time.Now()}))
	metadata, _, ok := h.store.Locale(value.ID, "ko")
	require.True(t, ok)
	require.Equal(t, "Album", *metadata.Title, "the source title is the protected target title")
	records := h.audit.Records()[before:]
	require.Len(t, records, 1)
	require.Equal(t, h.principal.memberID, records[0].MemberID)
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, records[0].ItemOperation)

	candidateOfDeleted := candidate(value.ID)
	require.NoError(t, h.store.DeleteRelease(t.Context(), value.ID))
	err = release.ApplyTypedTranslationCandidate(t.Context(), joined, job, candidateOfDeleted, translation.EntryWrite{Now: time.Now()})
	require.ErrorIs(t, err, release.ErrLocaleNotFound)
	require.Len(t, h.audit.Records(), before+1)
}

func releaseMember(nickname string) releasetest.Member {
	return releasetest.Member{Nickname: nickname}
}
