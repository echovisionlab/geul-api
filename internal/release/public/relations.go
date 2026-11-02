package public

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/release"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// loadReleaseArtists gets actual artists for every Release in one relation query.
func loadReleaseArtists(ctx context.Context, store Store, releaseIDs []string) (map[string][]Artist, error) {
	artists, err := store.ReleaseArtists(ctx, releaseIDs)
	if err != nil {
		return nil, err
	}
	artistIDs := make([]string, 0, len(artists))
	for _, artist := range artists {
		artistIDs = append(artistIDs, artist.ArtistID)
	}
	summaries, err := store.ArtistSummaries(ctx, artistIDs)
	if err != nil {
		return nil, err
	}
	imageFileIDs := make([]string, 0, len(artists))
	for _, artist := range artists {
		if artist.ImageFileID != nil {
			imageFileIDs = append(imageFileIDs, *artist.ImageFileID)
		}
	}
	imageAssets, err := store.ReadySourceAssets(ctx, imageFileIDs, "image")
	if err != nil {
		return nil, err
	}
	result := make(map[string][]Artist, len(releaseIDs))
	for _, releaseArtist := range artists {
		summary := summaries[releaseArtist.ArtistID]
		artist := Artist{ID: releaseArtist.ArtistID, Name: summary.Title, Role: "primary"}
		if summary.Slug != "" {
			artist.Slug = &summary.Slug
		}
		if releaseArtist.ImageFileID != nil {
			if asset, ok := imageAssets[*releaseArtist.ImageFileID]; ok {
				artist.Image = asset
			}
		}
		result[releaseArtist.ReleaseID] = append(result[releaseArtist.ReleaseID], artist)
	}
	return result, nil
}

// loadReleaseLabels gets release labels.
func loadReleaseLabels(ctx context.Context, store Store, releaseID string) ([]Label, error) {
	labels, err := store.ReleaseLabels(ctx, releaseID)
	if err != nil {
		return nil, err
	}
	labelIDs := make([]string, 0, len(labels))
	for _, label := range labels {
		labelIDs = append(labelIDs, label.ID)
	}
	summaries, err := store.LabelSummaries(ctx, labelIDs)
	if err != nil {
		return nil, err
	}
	result := make([]Label, 0, len(labels))
	for _, label := range labels {
		summary := summaries[label.ID]
		projected := Label{ID: label.ID, Name: summary.Title, CatalogNumber: label.CatalogNumber}
		if summary.Slug != "" {
			projected.Slug = &summary.Slug
		}
		result = append(result, projected)
	}
	return result, nil
}

// releaseCreditProjection is what a credit's display needs besides its row:
// its Artist summary, its Member, its image and its note.
type releaseCreditProjection struct {
	artists        map[string]release.ArtistSummary
	members        map[string]*memberref.Summary
	imageAssets    map[string]*mediaasset.Reference
	noteByCreditID map[string]string
}

func (s *Service) loadReleaseCredits(
	ctx context.Context,
	store Store,
	releaseID string,
	sourceLocale string,
	displayedLocale string,
) ([]Credit, error) {
	credits, err := store.Credits(ctx, releaseID)
	if err != nil {
		return nil, err
	}
	artistIDs := make([]string, 0, len(credits))
	for _, credit := range credits {
		if credit.ArtistID != nil {
			artistIDs = append(artistIDs, *credit.ArtistID)
		}
	}
	projection := releaseCreditProjection{}
	if projection.artists, err = store.ArtistSummaries(ctx, artistIDs); err != nil {
		return nil, err
	}
	creditIDs := make([]string, 0, len(credits))
	memberIDs := make([]string, 0, len(credits))
	imageFileIDs := make([]string, 0, len(credits))
	for _, credit := range credits {
		creditIDs = append(creditIDs, credit.ID)
		if credit.MemberID != nil {
			memberIDs = append(memberIDs, *credit.MemberID)
		}
		if credit.ImageFileID != nil {
			imageFileIDs = append(imageFileIDs, *credit.ImageFileID)
		}
	}
	if projection.members, err = s.members.LoadMemberSummaries(ctx, memberIDs); err != nil {
		return nil, err
	}
	if projection.imageAssets, err = store.ReadySourceAssets(ctx, imageFileIDs, "image"); err != nil {
		return nil, err
	}
	if projection.noteByCreditID, err = creditNotesForDisplay(ctx, store, releaseID, sourceLocale, displayedLocale, creditIDs); err != nil {
		return nil, err
	}
	result := make([]Credit, 0, len(credits))
	for _, credit := range credits {
		if projected, ok := projectReleaseCredit(credit, projection); ok {
			result = append(result, projected)
		}
	}
	return result, nil
}

// creditNotesForDisplay overlays the displayed locale on the source-locale
// credit notes.
func creditNotesForDisplay(ctx context.Context, store Store, releaseID, sourceLocale, displayedLocale string, creditIDs []string) (map[string]string, error) {
	source, err := store.CreditNotes(ctx, releaseID, sourceLocale)
	if err != nil {
		return nil, err
	}
	localized := source
	if displayedLocale != sourceLocale {
		localized, err = store.CreditNotes(ctx, releaseID, displayedLocale)
		if err != nil {
			return nil, err
		}
	}
	return release.MergeCreditNotesForDisplay(source, localized, creditIDs), nil
}

// projectReleaseCredit shows a credit by its Artist, Member or credited
// name, in that order; a credit with none of them is not shown.
func projectReleaseCredit(credit CreditRow, projection releaseCreditProjection) (Credit, bool) {
	var artistName, slug *string
	if credit.ArtistID != nil {
		summary := projection.artists[*credit.ArtistID]
		if summary.Title != "" {
			artistName = &summary.Title
		}
		if summary.Slug != "" {
			slug = &summary.Slug
		}
	}
	name := ""
	switch {
	case artistName != nil:
		name = *artistName
	case credit.MemberID != nil && projection.members[*credit.MemberID] != nil:
		name = projection.members[*credit.MemberID].Nickname
	case credit.CreditedName != nil:
		name = *credit.CreditedName
	}
	if name == "" {
		return Credit{}, false
	}
	projected := Credit{
		ID: credit.ID, Name: name, Slug: slug, CreditRole: credit.CreditRole,
		ArtistID: credit.ArtistID, MemberID: credit.MemberID,
	}
	if credit.MemberID != nil {
		projected.Member = projection.members[*credit.MemberID]
	}
	if note, ok := projection.noteByCreditID[credit.ID]; ok {
		projected.Note = &note
	}
	switch {
	case credit.ImageFileID != nil:
		projected.Image = projection.imageAssets[*credit.ImageFileID]
	case projected.Member != nil:
		projected.Image = projected.Member.Avatar.Reference()
	}
	return projected, true
}

// loadReleaseTracks gets the tracks of a release with their media and
// download access.
func (s *Service) loadReleaseTracks(
	ctx context.Context,
	store Store,
	releaseID string,
	releaseStatus string,
	mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization,
) ([]Track, error) {
	tracks, err := store.Tracks(ctx, releaseID)
	if err != nil {
		return nil, err
	}
	audioFileIDs := make([]string, 0, len(tracks))
	downloadRequests := make([]TrackDownloadAccessRequest, 0, len(tracks))
	for _, track := range tracks {
		if track.AudioOriginalFileID != nil && *track.AudioOriginalFileID != "" {
			audioFileIDs = append(audioFileIDs, *track.AudioOriginalFileID)
			downloadRequests = append(downloadRequests, TrackDownloadAccessRequest{
				TrackID: track.ID, FileID: *track.AudioOriginalFileID,
			})
		}
	}
	var sources releaseTrackMediaSources
	if sources.originalFiles, err = store.TrackFiles(ctx, audioFileIDs); err != nil {
		return nil, err
	}
	if sources.waveformRefs, err = store.TrackWaveforms(ctx, audioFileIDs); err != nil {
		return nil, err
	}
	if sources.spectrogramRefs, err = store.TrackSpectrograms(ctx, audioFileIDs); err != nil {
		return nil, err
	}
	if sources.playbackRefs, err = store.TrackHLS(ctx, audioFileIDs); err != nil {
		return nil, err
	}
	sources.downloadByTrackID, err = s.downloads.Resolve(ctx, releaseID, releaseStatus, mediaAuthorization, downloadRequests, s.signer.DownloadRef)
	if err != nil {
		return nil, err
	}
	trackIDs := make([]string, 0, len(tracks))
	for _, track := range tracks {
		trackIDs = append(trackIDs, track.ID)
	}
	creditsByTrack, err := loadTrackCredits(ctx, store, trackIDs)
	if err != nil {
		return nil, err
	}
	result := make([]Track, 0, len(tracks))
	for _, track := range tracks {
		media, download := resolveReleaseTrackMedia(track, sources)
		result = append(result, Track{
			ID: track.ID, Title: track.Title, TrackNumber: track.TrackNumber, DurationSeconds: track.DurationSeconds,
			Credits: creditsByTrack[track.ID], Media: media, Download: download,
		})
	}
	return result, nil
}

type releaseTrackMediaSources struct {
	originalFiles     map[string]MediaFile
	playbackRefs      map[string]*commonv1.HlsMediaRef
	waveformRefs      map[string]*mediaasset.Reference
	spectrogramRefs   map[string]*mediaasset.Reference
	downloadByTrackID map[string]TrackDownloadAuthorization
}

func resolveReleaseTrackMedia(track release.Track, sources releaseTrackMediaSources) (TrackMedia, DownloadAccess) {
	media := TrackMedia{}
	download := unavailableDownloadAccess
	if track.AudioOriginalFileID == nil || *track.AudioOriginalFileID == "" {
		return media, download
	}
	fileID := *track.AudioOriginalFileID
	media.Source = sources.originalFiles[fileID]
	media.Playback = sources.playbackRefs[fileID]
	media.Waveform = sources.waveformRefs[fileID]
	media.Spectrogram = sources.spectrogramRefs[fileID]
	if resolved, exists := sources.downloadByTrackID[track.ID]; exists && resolved.Access != nil {
		download = *resolved.Access
		media.Download = resolved.Download
	}
	return media, download
}

// loadTrackCredits gets credits for every Track in one relation query.
func loadTrackCredits(ctx context.Context, store Store, trackIDs []string) (map[string][]TrackCredit, error) {
	credits, err := store.TrackCredits(ctx, trackIDs)
	if err != nil {
		return nil, err
	}
	artistIDs := make([]string, 0, len(credits))
	for _, credit := range credits {
		if credit.ArtistID != nil {
			artistIDs = append(artistIDs, *credit.ArtistID)
		}
	}
	summaries, err := store.ArtistSummaries(ctx, artistIDs)
	if err != nil {
		return nil, err
	}
	imageFileIDs := make([]string, 0, len(credits))
	for _, credit := range credits {
		if credit.ImageFileID != nil {
			imageFileIDs = append(imageFileIDs, *credit.ImageFileID)
		}
	}
	imageAssets, err := store.ReadySourceAssets(ctx, imageFileIDs, "image")
	if err != nil {
		return nil, err
	}
	result := make(map[string][]TrackCredit, len(trackIDs))
	for _, credit := range credits {
		projected := TrackCredit{ID: credit.ID, Name: credit.CreditedName, CreditRole: credit.RoleName}
		if credit.ArtistID != nil {
			summary := summaries[*credit.ArtistID]
			artist := &Artist{ID: *credit.ArtistID, Name: summary.Title, Role: "credit"}
			if summary.Slug != "" {
				artist.Slug = &summary.Slug
			}
			if credit.ImageFileID != nil {
				artist.Image = imageAssets[*credit.ImageFileID]
			}
			projected.Artist = artist
		}
		result[credit.TrackID] = append(result[credit.TrackID], projected)
	}
	return result, nil
}
