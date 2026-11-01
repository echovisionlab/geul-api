package filemediatest

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

func (s *LibraryStore) LockIngestFile(ctx context.Context, id string) error {
	_, err := s.PromotionFile(ctx, id, true)
	if err != nil {
		return err
	}
	s.MediaAssets.PutFile(id, nil)
	return nil
}

func (s *LibraryStore) EnsureIngestGeneration(ctx context.Context, input mediaasset.Generation) (mediaasset.Generation, error) {
	if s.Fail != nil {
		return mediaasset.Generation{}, s.Fail
	}
	if _, exists := s.Generations[input.ID]; !exists {
		if _, ok := s.Files[input.FileID]; !ok {
			return mediaasset.Generation{}, domain.ErrFileNotFound
		}
		for _, existing := range s.Generations {
			if existing.ObjectPrefix == input.ObjectPrefix {
				return mediaasset.Generation{}, mediaasset.ErrGenerationNotFound
			}
		}
		s.PutMediaGeneration(input)
	}
	return s.MediaAssets.Generation(ctx, input.ID)
}

func (s *LibraryStore) EnsureIngestAsset(ctx context.Context, input mediaasset.Asset) (mediaasset.Asset, error) {
	if s.Fail != nil {
		return mediaasset.Asset{}, s.Fail
	}
	if input.SourceFileID != nil {
		if _, ok := s.Files[*input.SourceFileID]; !ok {
			return mediaasset.Asset{}, domain.ErrFileNotFound
		}
	}
	asset, err := s.MediaAssets.EnsureAssetAllocation(ctx, input)
	if err == nil {
		s.AssetIDs[asset.ID] = true
	}
	return asset, err
}

func (s *LibraryStore) CompleteIngestGeneration(ctx context.Context, cdn string, input mediaasset.WrittenGeneration) error {
	if s.Fail != nil {
		return s.Fail
	}
	generation, err := mediaasset.NewLifecycle(mutationtest.New[mediaasset.Store](s.MediaAssets), cdn).CompleteMediaGeneration(ctx, &input)
	if err == nil {
		s.Generations[generation.ID] = *generation
	}
	return err
}

func (s *LibraryStore) CompleteIngestAsset(ctx context.Context, cdn string, input mediaasset.WrittenAsset) error {
	if s.Fail != nil {
		return s.Fail
	}
	_, err := mediaasset.NewLifecycle(mutationtest.New[mediaasset.Store](s.MediaAssets), cdn).CompletePublicAsset(ctx, &input)
	return err
}

func (s *LibraryStore) UpsertIngestDerivative(ctx context.Context, input domain.IngestDerivative) error {
	if s.Fail != nil {
		return s.Fail
	}
	if _, ok := s.Files[input.FileID]; !ok {
		return domain.ErrFileNotFound
	}
	if input.AssetID != nil {
		if _, err := s.MediaAssets.Asset(ctx, *input.AssetID); err != nil {
			return err
		}
	}
	if input.GenerationID != nil {
		if _, err := s.MediaAssets.Generation(ctx, *input.GenerationID); err != nil {
			return err
		}
	}
	if (input.AssetID == nil) == (input.GenerationID == nil) {
		return errors.New("ingest derivative must have one output")
	}
	row := domain.StoredDerivativeDelivery{FileID: input.FileID, Type: input.Type, AssetID: cloneUploadPointer(input.AssetID), MediaGenerationID: cloneUploadPointer(input.GenerationID)}
	rows := s.Derivatives[input.FileID]
	for i, old := range rows {
		if old.Type == input.Type {
			rows[i] = row
			s.Derivatives[input.FileID] = rows
			return nil
		}
	}
	s.Derivatives[input.FileID] = append(rows, row)
	return nil
}

func (s *LibraryStore) SetClientMediaBundle(_ context.Context, file, bundle string, duration int) error {
	if s.Fail != nil {
		return s.Fail
	}
	row, ok := s.Files[file]
	if ok && !row.PendingDeletion {
		row.ClientMediaBundleID = new(bundle)
		row.Row.DurationSeconds = new(int32(duration))
		row.Row.UpdatedAt = time.Now().UTC()
		s.Files[file] = row
	}
	return nil
}

func (s *LibraryStore) ReadyIngestDerivativeCount(ctx context.Context, file string, required []string) (int64, error) {
	if s.Fail != nil {
		return 0, s.Fail
	}
	var count int64
	for _, row := range s.Derivatives[file] {
		if !slices.Contains(required, row.Type) {
			continue
		}
		ready := false
		if row.MediaGenerationID != nil {
			generation, err := s.MediaAssets.Generation(ctx, *row.MediaGenerationID)
			if err != nil && !errors.Is(err, mediaasset.ErrGenerationNotFound) {
				return 0, err
			}
			ready = err == nil && generation.FileID == file && generation.Status == mediaasset.GenerationReady
		}
		if row.AssetID != nil {
			asset, err := s.MediaAssets.Asset(ctx, *row.AssetID)
			if err != nil && !errors.Is(err, mediaasset.ErrAssetNotFound) {
				return 0, err
			}
			ready = ready || (err == nil && asset.SourceFileID != nil && *asset.SourceFileID == file && asset.Status == mediaasset.AssetReady)
		}
		if ready {
			count++
		}
	}
	return count, nil
}

func (s *LibraryStore) AttachIngestTrackAudio(ctx context.Context, input domain.TrackOriginalAudioInput) (domain.TrackOriginalAudioAttachment, error) {
	if err := s.RequireUploadTrack(ctx, input.TrackID); err != nil {
		return domain.TrackOriginalAudioAttachment{}, err
	}
	current := s.TrackOriginals[input.TrackID]
	output := domain.TrackOriginalAudioAttachment{ReleaseID: s.TrackReleases[input.TrackID], CurrentFileID: current}
	if current == input.VerifiedFileID {
		output.AlreadyApplied = true
		return output, nil
	}
	expected := ""
	if input.ExpectedCurrentFileID != nil {
		expected = *input.ExpectedCurrentFileID
	}
	if current != expected {
		return domain.TrackOriginalAudioAttachment{}, domainerr.Precondition("Track original audio changed before attachment")
	}
	if s.TrackOriginals == nil {
		s.TrackOriginals = map[string]string{}
	}
	s.TrackOriginals[input.TrackID] = input.VerifiedFileID
	output.CurrentFileID = input.VerifiedFileID
	return output, nil
}

func (s *LibraryStore) RequireIngestTranscodePublisher(context.Context) error {
	if s.Fail != nil {
		return s.Fail
	}
	return s.IngestTranscodeError
}

func (s *LibraryStore) AttachedIngestTrackRelease(_ context.Context, track, file string) (string, error) {
	if s.Fail != nil {
		return "", s.Fail
	}
	if s.TrackOriginals[track] != file {
		return "", domain.ErrUploadTrackNotFound
	}
	release, ok := s.TrackReleases[track]
	if !ok {
		return "", domain.ErrUploadTrackNotFound
	}
	return release, nil
}

func (s *LibraryStore) EnqueueIngestTranscode(ctx context.Context, input domain.IngestTranscodeCommand) error {
	if err := s.RequireIngestTranscodePublisher(ctx); err != nil {
		return err
	}
	if input.Source != nil {
		input.Source = new(*input.Source)
	}
	if input.HLSOutput != nil {
		input.HLSOutput = new(*input.HLSOutput)
	}
	if input.ArtifactOutput != nil {
		input.ArtifactOutput = new(*input.ArtifactOutput)
		input.ArtifactOutput.DownloadFilename = cloneUploadPointer(input.ArtifactOutput.DownloadFilename)
	}
	s.IngestCommands = append(s.IngestCommands, input)
	return nil
}
