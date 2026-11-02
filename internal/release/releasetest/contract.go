package releasetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/release"
)

// Harness is one release.Store implementation the contract runs against,
// with the rows of other domains a case needs.
type Harness struct {
	mutationtest.Harness[release.Store]
	// SeedDocument stores an empty compact content document and returns its
	// ID.
	SeedDocument func(t *testing.T) string
	// SeedArtist and SeedLabel store an Artist and a Label and return their
	// IDs.
	SeedArtist func(t *testing.T) string
	SeedLabel  func(t *testing.T) string
	// SeedReference stores a catalog reference of the relation named name,
	// and SeedFormat a Format; both return its ID.
	SeedReference func(t *testing.T, relation release.ReferenceRelation, name string) string
	SeedFormat    func(t *testing.T, name string) string
	// SeedMember stores a live Member and returns its ID.
	SeedMember func(t *testing.T, nickname string, preferredLocale *string) string
	// SeedFile stores a File and returns its ID.
	SeedFile func(t *testing.T) string
	// SeedRestrictedDownload gives the Track an original audio File and a
	// restricted download audience with one segment.
	SeedRestrictedDownload func(t *testing.T, trackID string)
	// SeedUploadSession opens an audio upload session of the Track.
	SeedUploadSession func(t *testing.T, trackID string)
}

// FakeHarness is the Harness of the fake Store.
func FakeHarness() Harness {
	store := NewStore()
	return Harness{
		Harness:      mutationtest.FakeHarness[release.Store](store),
		SeedDocument: func(*testing.T) string { return uuid.NewString() },
		SeedArtist: func(*testing.T) string {
			id := uuid.NewString()
			store.mu.Lock()
			store.Artists[id] = release.Summary{}
			store.mu.Unlock()
			return id
		},
		SeedLabel: func(*testing.T) string {
			id := uuid.NewString()
			store.mu.Lock()
			store.Labels[id] = release.Summary{}
			store.mu.Unlock()
			return id
		},
		SeedReference: func(_ *testing.T, _ release.ReferenceRelation, name string) string {
			id := uuid.NewString()
			store.mu.Lock()
			store.Catalog[id] = release.Reference{ID: id, Name: name, Slug: "slug-" + id}
			store.mu.Unlock()
			return id
		},
		SeedFormat: func(_ *testing.T, name string) string {
			id := uuid.NewString()
			store.mu.Lock()
			store.Catalog[id] = release.Reference{ID: id, Name: name, Slug: "slug-" + id}
			store.mu.Unlock()
			return id
		},
		SeedMember: func(_ *testing.T, nickname string, preferredLocale *string) string {
			id := uuid.NewString()
			store.mu.Lock()
			store.Members[id] = Member{Nickname: nickname, PreferredLocale: preferredLocale}
			store.mu.Unlock()
			return id
		},
		SeedFile: func(*testing.T) string {
			id := uuid.NewString()
			store.PutFile(id, id)
			return id
		},
		SeedRestrictedDownload: func(_ *testing.T, trackID string) {
			track, _ := store.Track(trackID)
			track.AudioOriginalFileID, track.DownloadAudience = new(uuid.NewString()), "restricted"
			store.PutTrack(track)
			store.PutTrackSegments(trackID, uuid.NewString())
		},
		SeedUploadSession: func(_ *testing.T, trackID string) { store.SetUploadSession(trackID, true) },
	}
}

// RunStoreContract runs every Store contract case against a fresh Harness,
// in parallel.
func RunStoreContract(t *testing.T, open func(t *testing.T) Harness) {
	t.Helper()
	for name, run := range storeContract {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run(t, open(t))
		})
	}
}

func transact(t *testing.T, h Harness, work func(ctx context.Context, store release.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h.Harness, work))
}

// insert stores a draft album in its own transaction.
func insert(t *testing.T, h Harness, change func(*release.Release)) release.Release {
	t.Helper()
	documentID := h.SeedDocument(t)
	value := release.Release{SourceLocale: "en", Type: release.TypeAlbum, Status: release.StatusDraft, ContentDocumentID: &documentID}
	if change != nil {
		change(&value)
	}
	transact(t, h, func(ctx context.Context, store release.Store) error { return store.InsertRelease(ctx, &value) })
	return value
}

// insertTrack stores a Track numbered next in its own transaction.
func insertTrack(t *testing.T, h Harness, releaseID, title string) release.Track {
	t.Helper()
	var track release.Track
	transact(t, h, func(ctx context.Context, store release.Store) error {
		number, err := store.NextTrackNumber(ctx, releaseID)
		if err != nil {
			return err
		}
		track = release.Track{ReleaseID: releaseID, TrackNumber: number, Title: title, Metadata: map[string]any{}}
		return store.InsertTrack(ctx, &track)
	})
	return track
}

func text(value string) *string { return &value }

var errRolledBack = errors.New("roll back")

var storeContract = map[string]func(t *testing.T, h Harness){
	"a missing release answers its sentinels": func(t *testing.T, h Harness) {
		missing := uuid.NewString()
		transact(t, h, func(ctx context.Context, store release.Store) error {
			_, err := store.FindRelease(ctx, missing)
			require.ErrorIs(t, err, release.ErrReleaseNotFound)
			_, err = store.LockRelease(ctx, missing, release.UpdateLock)
			require.ErrorIs(t, err, release.ErrReleaseNotFound)
			require.ErrorIs(t, store.ReleaseExists(ctx, missing), release.ErrReleaseNotFound)
			_, err = store.ContentDocumentID(ctx, missing, release.ShareLock)
			require.ErrorIs(t, err, release.ErrReleaseNotFound)
			_, err = store.SourceLocale(ctx, missing, release.UpdateLock)
			require.ErrorIs(t, err, release.ErrReleaseNotFound)
			_, err = store.DocumentRoot(ctx, missing, release.KeyShareLock)
			require.ErrorIs(t, err, release.ErrReleaseNotFound)
			require.ErrorIs(t, store.TouchRelease(ctx, missing, time.Now()), release.ErrReleaseNotFound)
			require.ErrorIs(t, store.UpdateReleaseSettings(ctx, missing, release.Settings{}, time.Now()), release.ErrReleaseNotFound)
			require.ErrorIs(t, store.DeleteRelease(ctx, missing), release.ErrReleaseNotFound)
			return nil
		})
	},
	"an inserted release reads back with its root": func(t *testing.T, h Harness) {
		inserted := insert(t, h, func(value *release.Release) {
			value.Slug, value.CatalogNumber, value.Type = text("insert-"+uuid.NewString()), text("CAT-1"), release.TypeEP
		})
		require.NotEmpty(t, inserted.ID)
		require.False(t, inserted.CreatedAt.IsZero())
		transact(t, h, func(ctx context.Context, store release.Store) error {
			found, err := store.FindRelease(ctx, inserted.ID)
			require.NoError(t, err)
			require.Equal(t, *inserted.Slug, *found.Slug)
			require.Equal(t, "CAT-1", *found.CatalogNumber)
			require.Equal(t, release.TypeEP, found.Type)
			require.Equal(t, release.StatusDraft, found.Status)
			documentID, err := store.ContentDocumentID(ctx, inserted.ID, release.UpdateLock)
			require.NoError(t, err)
			require.Equal(t, *inserted.ContentDocumentID, documentID.String())
			sourceLocale, err := store.SourceLocale(ctx, inserted.ID, release.ShareLock)
			require.NoError(t, err)
			require.Equal(t, "en", sourceLocale)
			root, err := store.DocumentRoot(ctx, inserted.ID, release.KeyShareLock)
			require.NoError(t, err)
			require.Equal(t, release.DocumentRoot{ID: inserted.ID, Status: release.StatusDraft, SourceLocale: "en", DocumentID: documentID}, root)
			return store.ReleaseExists(ctx, inserted.ID)
		})
	},
	"a slug another release holds is taken": func(t *testing.T, h Harness) {
		slug := "taken-" + uuid.NewString()
		first := insert(t, h, func(value *release.Release) { value.Slug = &slug })
		second := insert(t, h, nil)
		documentID := h.SeedDocument(t)
		duplicate := release.Release{SourceLocale: "en", Type: release.TypeAlbum, Status: release.StatusDraft, Slug: &slug, ContentDocumentID: &documentID}
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store release.Store) error {
			return store.InsertRelease(ctx, &duplicate)
		})
		require.ErrorIs(t, err, release.ErrSlugTaken)
		err = mutationtest.Transact(t, h.Harness, func(ctx context.Context, store release.Store) error {
			return store.UpdateReleaseSettings(ctx, second.ID, release.Settings{Slug: &slug}, time.Now())
		})
		require.ErrorIs(t, err, release.ErrSlugTaken)
		transact(t, h, func(ctx context.Context, store release.Store) error {
			taken, err := store.SlugTaken(ctx, slug, nil)
			require.NoError(t, err)
			require.True(t, taken)
			taken, err = store.SlugTaken(ctx, slug, &first.ID)
			require.NoError(t, err)
			require.False(t, taken)
			return nil
		})
	},
	"settings, status and touch store what they name": func(t *testing.T, h Harness) {
		date := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		inserted := insert(t, h, func(value *release.Release) {
			value.ReleaseDate, value.SpotifyURL = &date, text("https://spotify.test/old")
		})
		now := time.Now().UTC().Truncate(time.Microsecond)
		albumType := release.TypeSingle
		transact(t, h, func(ctx context.Context, store release.Store) error {
			return store.UpdateReleaseSettings(ctx, inserted.ID, release.Settings{
				Type: &albumType, ReleaseDateSet: true, BandcampURL: text("https://bandcamp.test"),
			}, now)
		})
		published := now.Add(time.Minute)
		transact(t, h, func(ctx context.Context, store release.Store) error {
			found, err := store.FindRelease(ctx, inserted.ID)
			require.NoError(t, err)
			require.Equal(t, release.TypeSingle, found.Type)
			require.Nil(t, found.ReleaseDate)
			require.Equal(t, "https://spotify.test/old", *found.SpotifyURL)
			require.Equal(t, "https://bandcamp.test", *found.BandcampURL)
			require.True(t, now.Equal(found.UpdatedAt))
			return store.SetReleaseStatus(ctx, inserted.ID, release.StatusPublished, &published, published)
		})
		transact(t, h, func(ctx context.Context, store release.Store) error {
			found, err := store.FindRelease(ctx, inserted.ID)
			require.NoError(t, err)
			require.Equal(t, release.StatusPublished, found.Status)
			require.True(t, published.Equal(*found.PublishedAt))
			touched := published.Add(time.Minute)
			require.NoError(t, store.TouchRelease(ctx, inserted.ID, touched))
			found, err = store.FindRelease(ctx, inserted.ID)
			require.NoError(t, err)
			require.True(t, touched.Equal(found.UpdatedAt))
			return nil
		})
	},
	"the release list pages newest first and counts every release": func(t *testing.T, h Harness) {
		var inserted []release.Release
		for range 3 {
			inserted = append(inserted, insert(t, h, nil))
		}
		transact(t, h, func(ctx context.Context, store release.Store) error {
			page, total, err := store.ListReleases(ctx, release.ListPage{Limit: 2, Offset: 1})
			require.NoError(t, err)
			require.Equal(t, int64(3), total)
			require.Equal(t, []string{inserted[1].ID, inserted[0].ID}, []string{page[0].ID, page[1].ID})
			return nil
		})
	},
	"a locked release makes a second writer wait for the first commit": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		catalogNumber := "AFTER-LOCK"
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store release.Store) error {
				_, err := store.LockRelease(ctx, inserted.ID, release.UpdateLock)
				return err
			},
			func(ctx context.Context, store release.Store) error {
				return store.UpdateReleaseSettings(ctx, inserted.ID, release.Settings{CatalogNumber: &catalogNumber}, time.Now())
			},
			func(ctx context.Context, store release.Store) error {
				locked, err := store.LockRelease(ctx, inserted.ID, release.UpdateLock)
				if err != nil {
					return err
				}
				if locked.CatalogNumber == nil || *locked.CatalogNumber != catalogNumber {
					return errors.New("the second writer read the release before the first commit")
				}
				return nil
			},
		)
		require.NoError(t, err)
	},
	"a failed transaction leaves no release behind": func(t *testing.T, h Harness) {
		documentID := h.SeedDocument(t)
		value := release.Release{SourceLocale: "en", Type: release.TypeAlbum, Status: release.StatusDraft, ContentDocumentID: &documentID}
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store release.Store) error {
			if err := store.InsertRelease(ctx, &value); err != nil {
				return err
			}
			return errRolledBack
		})
		require.ErrorIs(t, err, errRolledBack)
		transact(t, h, func(ctx context.Context, store release.Store) error {
			_, err := store.FindRelease(ctx, value.ID)
			require.ErrorIs(t, err, release.ErrReleaseNotFound)
			return nil
		})
	},
	"locales store titles and are created, updated and deleted once": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		now := time.Now().UTC().Truncate(time.Microsecond)
		transact(t, h, func(ctx context.Context, store release.Store) error {
			_, exists, err := store.LocaleMetadata(ctx, inserted.ID, "en", false)
			require.NoError(t, err)
			require.False(t, exists)
			_, err = store.LocaleTitle(ctx, inserted.ID, "en")
			require.ErrorIs(t, err, release.ErrLocaleNotFound)
			require.NoError(t, store.SaveSourceLocaleTitle(ctx, inserted.ID, "en", "First", now))
			require.NoError(t, store.SaveSourceLocaleTitle(ctx, inserted.ID, "en", "Second", now))
			metadata, exists, err := store.LocaleMetadata(ctx, inserted.ID, "en", true)
			require.NoError(t, err)
			require.True(t, exists)
			require.Equal(t, "Second", *metadata.Title)
			require.True(t, now.Equal(metadata.UpdatedAt))
			require.NoError(t, store.InsertLocale(ctx, inserted.ID, "ko", nil, now))
			title, err := store.LocaleTitle(ctx, inserted.ID, "ko")
			require.NoError(t, err)
			require.Nil(t, title)
			updated, err := store.UpdateLocaleTitle(ctx, inserted.ID, "ko", text("Korean"), now.Add(time.Second))
			require.NoError(t, err)
			require.True(t, updated)
			updated, err = store.UpdateLocaleTitle(ctx, inserted.ID, "ja", text("Japanese"), now)
			require.NoError(t, err)
			require.False(t, updated)
			deleted, err := store.DeleteLocale(ctx, inserted.ID, "ko")
			require.NoError(t, err)
			require.True(t, deleted)
			deleted, err = store.DeleteLocale(ctx, inserted.ID, "ko")
			require.NoError(t, err)
			require.False(t, deleted)
			return nil
		})
	},
	"credit notes belong to current credits of one locale": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		first, second := uuid.NewString(), uuid.NewString()
		now := time.Now().UTC()
		transact(t, h, func(ctx context.Context, store release.Store) error {
			credits := []release.ReleaseCredit{
				{ID: first, CreditedName: text("First"), SortOrder: 0},
				{ID: second, CreditedName: text("Second"), SortOrder: 1},
			}
			require.NoError(t, store.ReplaceReleaseCredits(ctx, inserted.ID, nil, credits))
			require.NoError(t, store.InsertLocale(ctx, inserted.ID, "ko", nil, now))
			require.NoError(t, store.SaveSourceLocaleTitle(ctx, inserted.ID, "en", "Title", now))
			require.NoError(t, store.ReplaceCreditNotes(ctx, inserted.ID, "en", map[string]string{first: " source ", second: "other"}, now))
			require.NoError(t, store.ReplaceCreditNotes(ctx, inserted.ID, "ko", map[string]string{first: "korean"}, now))
			require.NoError(t, store.ReplaceCreditNotes(ctx, inserted.ID, "en", map[string]string{first: "replaced"}, now))
			notes, err := store.CreditNotes(ctx, inserted.ID, "en")
			require.NoError(t, err)
			require.Equal(t, map[string]string{first: "replaced"}, notes)
			ids, err := store.CreditIDs(ctx, inserted.ID)
			require.NoError(t, err)
			require.ElementsMatch(t, []string{first, second}, ids)
			require.True(t, ids[0] < ids[1])
			require.NoError(t, store.ReplaceReleaseCredits(ctx, inserted.ID, credits, credits[1:]))
			notes, err = store.CreditNotes(ctx, inserted.ID, "ko")
			require.NoError(t, err)
			require.Empty(t, notes)
			deleted, err := store.DeleteLocale(ctx, inserted.ID, "en")
			require.NoError(t, err)
			require.True(t, deleted)
			return nil
		})
	},
	"relation rows replace in their order": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		a, b, c := h.SeedArtist(t), h.SeedArtist(t), h.SeedArtist(t)
		label := h.SeedLabel(t)
		format := h.SeedFormat(t, "Vinyl")
		transact(t, h, func(ctx context.Context, store release.Store) error {
			first := []release.ReleaseArtist{{ArtistID: c, SortOrder: 0}, {ArtistID: a, SortOrder: 1}}
			require.NoError(t, store.ReplaceReleaseArtists(ctx, inserted.ID, nil, first))
			next := []release.ReleaseArtist{{ArtistID: a, SortOrder: 0}, {ArtistID: b, SortOrder: 1}}
			require.NoError(t, store.ReplaceReleaseArtists(ctx, inserted.ID, first, next))
			artists, err := store.ReleaseArtists(ctx, inserted.ID)
			require.NoError(t, err)
			require.Equal(t, next, artists)
			byCreation, err := store.ReleaseArtistsByCreation(ctx, inserted.ID)
			require.NoError(t, err)
			require.Equal(t, next, byCreation)
			summaries, err := store.ArtistSummaries(ctx, []string{a, uuid.NewString()})
			require.NoError(t, err)
			require.Len(t, summaries, 1)
			require.Contains(t, summaries, a)

			labels := []release.ReleaseLabel{{LabelID: label, CatalogNumber: text("CAT"), SortOrder: 0}}
			require.NoError(t, store.ReplaceReleaseLabels(ctx, inserted.ID, nil, labels))
			changed := []release.ReleaseLabel{{LabelID: label, CatalogNumber: text("NEW"), SortOrder: 0}}
			require.NoError(t, store.ReplaceReleaseLabels(ctx, inserted.ID, labels, changed))
			storedLabels, err := store.ReleaseLabels(ctx, inserted.ID)
			require.NoError(t, err)
			require.Equal(t, changed, storedLabels)
			labelSummaries, err := store.LabelSummaries(ctx, []string{label})
			require.NoError(t, err)
			require.Contains(t, labelSummaries, label)

			formats := []release.ReleaseFormat{{FormatID: format, FormatDescription: text("180g")}}
			require.NoError(t, store.ReplaceReleaseFormats(ctx, inserted.ID, nil, formats))
			storedFormats, err := store.ReleaseFormats(ctx, inserted.ID)
			require.NoError(t, err)
			require.Equal(t, formats, storedFormats)
			named, err := store.Formats(ctx, inserted.ID)
			require.NoError(t, err)
			require.Len(t, named, 1)
			require.Equal(t, "Vinyl", named[0].Name)
			require.Equal(t, "180g", *named[0].Description)
			return nil
		})
	},
	"credits read back with their Member names in editor order": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		member := h.SeedMember(t, "Credited Member "+uuid.NewString()[:8], nil)
		artist := h.SeedArtist(t)
		first, second := uuid.NewString(), uuid.NewString()
		transact(t, h, func(ctx context.Context, store release.Store) error {
			credits := []release.ReleaseCredit{
				{ID: first, MemberID: &member, CreditRole: text("Mix"), SortOrder: 1},
				{ID: second, ArtistID: &artist, SortOrder: 0},
			}
			require.NoError(t, store.ReplaceReleaseCredits(ctx, inserted.ID, nil, credits))
			stored, err := store.ReleaseCredits(ctx, inserted.ID)
			require.NoError(t, err)
			require.Equal(t, []string{second, first}, []string{stored[0].ID, stored[1].ID})
			editor, err := store.EditorCredits(ctx, inserted.ID)
			require.NoError(t, err)
			require.Len(t, editor, 2)
			require.Equal(t, second, editor[0].ID)
			require.Nil(t, editor[0].MemberName)
			require.Equal(t, member, *editor[1].MemberID)
			require.NotNil(t, editor[1].MemberName)
			require.Equal(t, "Mix", *editor[1].CreditRole)
			return nil
		})
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store release.Store) error {
			return store.LockMemberReferences(ctx, []release.MemberReference{{MemberID: uuid.NewString(), Field: "credit[0].member_id"}})
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "credit[0].member_id")
	},
	"references link, unlink and read by name": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		for _, relation := range []release.ReferenceRelation{release.ReferenceCategories, release.ReferenceGenres, release.ReferenceStyles} {
			zeta, alpha := h.SeedReference(t, relation, "Zeta "+uuid.NewString()[:8]), h.SeedReference(t, relation, "Alpha "+uuid.NewString()[:8])
			transact(t, h, func(ctx context.Context, store release.Store) error {
				require.NoError(t, store.ReplaceReferences(ctx, relation, inserted.ID, nil, []string{zeta, alpha}))
				ids, err := store.ReferenceIDs(ctx, relation, inserted.ID)
				require.NoError(t, err)
				require.ElementsMatch(t, []string{zeta, alpha}, ids)
				references, err := store.References(ctx, relation, inserted.ID)
				require.NoError(t, err)
				require.Equal(t, []string{alpha, zeta}, []string{references[0].ID, references[1].ID})
				require.NoError(t, store.ReplaceReferences(ctx, relation, inserted.ID, []string{zeta}, nil))
				ids, err = store.ReferenceIDs(ctx, relation, inserted.ID)
				require.NoError(t, err)
				require.Equal(t, []string{alpha}, ids)
				return nil
			})
		}
	},
	"tracks number, renumber and count within their release": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		other := insert(t, h, nil)
		first := insertTrack(t, h, inserted.ID, "First")
		second := insertTrack(t, h, inserted.ID, "Second")
		insertTrack(t, h, other.ID, "Other")
		require.Equal(t, 1, first.TrackNumber)
		require.Equal(t, 2, second.TrackNumber)
		require.NotEmpty(t, first.ID)
		require.False(t, first.CreatedAt.IsZero())
		require.Equal(t, release.DownloadAudienceDisabled, first.DownloadAudience)
		require.NotNil(t, first.Metadata)
		transact(t, h, func(ctx context.Context, store release.Store) error {
			require.NoError(t, store.RenumberTracks(ctx, inserted.ID, []string{second.ID, first.ID}))
			order, err := store.TrackOrder(ctx, inserted.ID)
			require.NoError(t, err)
			require.Equal(t, []string{second.ID, first.ID}, order)
			count, err := store.CountTracksOf(ctx, inserted.ID, []string{first.ID, second.ID, uuid.NewString()})
			require.NoError(t, err)
			require.Equal(t, int64(2), count)
			total, err := store.CountReleaseTracks(ctx, inserted.ID)
			require.NoError(t, err)
			require.Equal(t, int64(2), total)
			counts, err := store.CountTracksPerRelease(ctx, []string{inserted.ID, other.ID})
			require.NoError(t, err)
			require.Equal(t, map[string]int32{inserted.ID: 2, other.ID: 1}, counts)
			locked, err := store.LockTrackIDs(ctx, inserted.ID)
			require.NoError(t, err)
			require.ElementsMatch(t, []string{first.ID, second.ID}, locked)
			saved := first
			saved.Title, saved.Lyrics = "Renamed", text("la la")
			saved.TrackNumber = 2
			require.NoError(t, store.SaveTrack(ctx, saved))
			found, err := store.FindTrack(ctx, first.ID)
			require.NoError(t, err)
			require.Equal(t, "Renamed", found.Title)
			require.Equal(t, "la la", *found.Lyrics)
			tracks, err := store.Tracks(ctx, inserted.ID)
			require.NoError(t, err)
			require.Equal(t, []string{second.ID, first.ID}, []string{tracks[0].ID, tracks[1].ID})
			return nil
		})
	},
	"a missing track answers its sentinel": func(t *testing.T, h Harness) {
		missing := uuid.NewString()
		transact(t, h, func(ctx context.Context, store release.Store) error {
			_, err := store.FindTrack(ctx, missing)
			require.ErrorIs(t, err, release.ErrTrackNotFound)
			_, err = store.LockTrack(ctx, missing)
			require.ErrorIs(t, err, release.ErrTrackNotFound)
			require.ErrorIs(t, store.LockTrackID(ctx, missing), release.ErrTrackNotFound)
			require.ErrorIs(t, store.DeleteTrack(ctx, missing), release.ErrTrackNotFound)
			return nil
		})
	},
	"a locked track makes a second writer wait for the first commit": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		track := insertTrack(t, h, inserted.ID, "Before")
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store release.Store) error {
				_, err := store.LockTrack(ctx, track.ID)
				return err
			},
			func(ctx context.Context, store release.Store) error {
				renamed := track
				renamed.Title = "After"
				return store.SaveTrack(ctx, renamed)
			},
			func(ctx context.Context, store release.Store) error {
				if err := store.LockTrackID(ctx, track.ID); err != nil {
					return err
				}
				found, err := store.FindTrack(ctx, track.ID)
				if err != nil {
					return err
				}
				if found.Title != "After" {
					return errors.New("the second writer read the track before the first commit")
				}
				return nil
			},
		)
		require.NoError(t, err)
	},
	"attaching audio and disabling a download forget the download segments": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		attached := insertTrack(t, h, inserted.ID, "Attached")
		disabled := insertTrack(t, h, inserted.ID, "Disabled")
		h.SeedRestrictedDownload(t, attached.ID)
		h.SeedRestrictedDownload(t, disabled.ID)
		fileID := h.SeedFile(t)
		duration := 42
		transact(t, h, func(ctx context.Context, store release.Store) error {
			require.NoError(t, store.LockTrackDownloadSegments(ctx, attached.ID))
			require.NoError(t, store.AttachTrackAudio(ctx, attached.ID, release.TrackAudio{FileID: fileID, Completed: true, DurationSeconds: &duration}))
			return store.DisableTrackDownload(ctx, disabled.ID)
		})
		transact(t, h, func(ctx context.Context, store release.Store) error {
			found, err := store.FindTrack(ctx, attached.ID)
			require.NoError(t, err)
			require.Equal(t, fileID, *found.AudioOriginalFileID)
			require.Equal(t, string(release.TrackProcessingCompleted), *found.ProcessingStatus)
			require.Equal(t, 42, *found.DurationSeconds)
			require.Equal(t, release.DownloadAudienceDisabled, found.DownloadAudience)
			found, err = store.FindTrack(ctx, disabled.ID)
			require.NoError(t, err)
			require.Equal(t, release.DownloadAudienceDisabled, found.DownloadAudience)
			return nil
		})
	},
	"track credits report their changes and read in order": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		track := insertTrack(t, h, inserted.ID, "Credited")
		artist := h.SeedArtist(t)
		var current []release.TrackCredit
		transact(t, h, func(ctx context.Context, store release.Store) error {
			changes, err := store.ReplaceTrackCredits(ctx, nil, []release.TrackCredit{
				{TrackID: track.ID, ArtistID: &artist, SortOrder: 0},
				{TrackID: track.ID, CreditedName: text("Text"), SortOrder: 1},
			})
			require.NoError(t, err)
			require.Len(t, changes, 2)
			for _, change := range changes {
				require.Equal(t, release.ChangeCreated, change.Kind)
				require.NotEmpty(t, change.CreditID)
			}
			current, err = store.TrackCredits(ctx, track.ID)
			require.NoError(t, err)
			require.Equal(t, []string{changes[0].CreditID, changes[1].CreditID}, []string{current[0].ID, current[1].ID})
			rows, err := store.TrackCreditRows(ctx, track.ID)
			require.NoError(t, err)
			require.Equal(t, []string{"artist", "text"}, []string{rows[0].CreditType, rows[1].CreditType})
			return nil
		})
		transact(t, h, func(ctx context.Context, store release.Store) error {
			changed := current[1]
			changed.CreditRole, changed.SortOrder = text("Vocals"), 0
			changes, err := store.ReplaceTrackCredits(ctx, current, []release.TrackCredit{changed})
			require.NoError(t, err)
			require.Equal(t, []release.TrackCreditChange{
				{CreditID: current[0].ID, Kind: release.ChangeDeleted},
				{CreditID: current[1].ID, Kind: release.ChangeUpdated},
			}, changes)
			stored, err := store.TrackCredits(ctx, track.ID)
			require.NoError(t, err)
			require.Len(t, stored, 1)
			require.Equal(t, "Vocals", *stored[0].CreditRole)
			return nil
		})
	},
	"upload sessions of a release's tracks are found": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		open := insertTrack(t, h, inserted.ID, "Uploading")
		idle := insertTrack(t, h, inserted.ID, "Idle")
		h.SeedUploadSession(t, open.ID)
		transact(t, h, func(ctx context.Context, store release.Store) error {
			found, err := store.HasTrackUploadSessions(ctx, []string{idle.ID})
			require.NoError(t, err)
			require.False(t, found)
			found, err = store.HasTrackUploadSessions(ctx, []string{idle.ID, open.ID})
			require.NoError(t, err)
			require.True(t, found)
			found, err = store.HasTrackUploadSessions(ctx, nil)
			require.NoError(t, err)
			require.False(t, found)
			return nil
		})
	},
	"artwork files replace and clear": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		first, second := h.SeedFile(t), h.SeedFile(t)
		transact(t, h, func(ctx context.Context, store release.Store) error {
			_, exists, err := store.ArtworkFile(ctx, inserted.ID)
			require.NoError(t, err)
			require.False(t, exists)
			stored, err := store.FileID(ctx, first)
			require.NoError(t, err)
			require.Equal(t, first, stored)
			_, err = store.FileID(ctx, uuid.NewString())
			require.ErrorIs(t, err, release.ErrFileNotFound)
			require.NoError(t, store.SetArtworkFile(ctx, inserted.ID, first))
			require.NoError(t, store.SetArtworkFile(ctx, inserted.ID, second))
			fileID, exists, err := store.ArtworkFile(ctx, inserted.ID)
			require.NoError(t, err)
			require.True(t, exists)
			require.Equal(t, second, fileID)
			require.NoError(t, store.ClearArtworkFiles(ctx, inserted.ID))
			_, exists, err = store.ArtworkFile(ctx, inserted.ID)
			require.NoError(t, err)
			require.False(t, exists)
			return nil
		})
	},
	"members lock and read their preferred locale": func(t *testing.T, h Harness) {
		member := h.SeedMember(t, "Locked Member "+uuid.NewString()[:8], text("ko"))
		transact(t, h, func(ctx context.Context, store release.Store) error {
			locked, err := store.LockMembers(ctx, []string{member, uuid.NewString()})
			require.NoError(t, err)
			require.Equal(t, []string{member}, locked)
			preferred, err := store.MemberPreferredLocale(ctx, member)
			require.NoError(t, err)
			require.Equal(t, "ko", *preferred)
			preferred, err = store.MemberPreferredLocale(ctx, uuid.NewString())
			require.NoError(t, err)
			require.Nil(t, preferred)
			return nil
		})
	},
	"deleting a release cascades to what it owns and keeps the library files": func(t *testing.T, h Harness) {
		inserted := insert(t, h, nil)
		track := insertTrack(t, h, inserted.ID, "Owned")
		artwork, original := h.SeedFile(t), h.SeedFile(t)
		transact(t, h, func(ctx context.Context, store release.Store) error {
			require.NoError(t, store.SetArtworkFile(ctx, inserted.ID, artwork))
			require.NoError(t, store.AttachTrackAudio(ctx, track.ID, release.TrackAudio{FileID: original}))
			require.NoError(t, store.SaveSourceLocaleTitle(ctx, inserted.ID, "en", "Owned", time.Now()))
			return store.ReplaceReleaseCredits(ctx, inserted.ID, nil, []release.ReleaseCredit{{ID: uuid.NewString(), CreditedName: text("Owned")}})
		})
		transact(t, h, func(ctx context.Context, store release.Store) error {
			counts, err := store.CountCreditsPerRelease(ctx, []string{inserted.ID})
			require.NoError(t, err)
			require.Equal(t, map[string]int32{inserted.ID: 1}, counts)
			return store.DeleteRelease(ctx, inserted.ID)
		})
		transact(t, h, func(ctx context.Context, store release.Store) error {
			_, err := store.FindTrack(ctx, track.ID)
			require.ErrorIs(t, err, release.ErrTrackNotFound)
			_, exists, err := store.LocaleMetadata(ctx, inserted.ID, "en", false)
			require.NoError(t, err)
			require.False(t, exists)
			for _, fileID := range []string{artwork, original} {
				stored, err := store.FileID(ctx, fileID)
				require.NoError(t, err)
				require.Equal(t, fileID, stored)
			}
			return nil
		})
	},
}
