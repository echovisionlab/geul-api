// Package applicationtest holds the in-memory Store of the transcode
// application and its Store contract.
package applicationtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/transcode"
	"github.com/echovisionlab/geul-api/internal/transcode/application"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// File is a stored File as the fake keeps it.
type File struct {
	application.File
	Extension       string
	DeleteRequested bool
}

// Generation is a stored media generation as the fake keeps it.
type Generation struct {
	application.MediaGeneration
	ManifestSHA256 []byte
	DeleteAfter    *time.Time
}

// Track is a stored Track's audio as the fake keeps it.
type Track struct {
	ReleaseID       string
	SourceFileID    string
	Status          string
	DurationSeconds *int
}

type derivativeKey struct{ fileID, derivativeType string }

// Store is an in-memory application.Store.
type Store struct {
	mu          sync.Mutex
	files       map[string]File
	assets      map[string]application.Asset
	bindings    map[string]int
	generations map[string]Generation
	derivatives map[derivativeKey]application.Derivative
	tracks      map[string]Track
	jobs        map[string]transcode.Job
	waveforms   map[string]transcode.WaveformJob
}

// New returns an empty fake Store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[application.Store]) {
	store := NewStore()
	return store, mutationtest.New[application.Store](store)
}

// NewStore returns an empty fake Store.
func NewStore() *Store {
	return &Store{
		files: map[string]File{}, assets: map[string]application.Asset{}, bindings: map[string]int{},
		generations: map[string]Generation{}, derivatives: map[derivativeKey]application.Derivative{},
		tracks: map[string]Track{}, jobs: map[string]transcode.Job{}, waveforms: map[string]transcode.WaveformJob{},
	}
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	files := mutationtest.ClonePersisted(s.files)
	assets := mutationtest.ClonePersisted(s.assets)
	bindings := mutationtest.ClonePersisted(s.bindings)
	generations := mutationtest.ClonePersisted(s.generations)
	derivatives := mutationtest.ClonePersisted(s.derivatives)
	tracks := mutationtest.ClonePersisted(s.tracks)
	jobs := mutationtest.ClonePersisted(s.jobs)
	waveforms := mutationtest.ClonePersisted(s.waveforms)
	return mutationtest.Checkpoint{
		State: map[string]any{
			"files":       files,
			"assets":      assets,
			"bindings":    bindings,
			"generations": generations,
			"derivatives": derivatives,
			"tracks":      tracks,
			"jobs":        jobs,
			"waveforms":   waveforms,
		},
		Restore: func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.files = files
			s.assets = assets
			s.bindings = bindings
			s.generations = generations
			s.derivatives = derivatives
			s.tracks = tracks
			s.jobs = jobs
			s.waveforms = waveforms
		},
	}
}

// SeedFile stores a File.
func (s *Store) SeedFile(file File) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[file.ID] = file
}

// RequestFileDeletion marks a File pending deletion.
func (s *Store) RequestFileDeletion(fileID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file := s.files[fileID]
	file.DeleteRequested = true
	s.files[fileID] = file
}

// SeedAllocatedAsset allocates a public asset of kind made from fileID.
func (s *Store) SeedAllocatedAsset(kind, fileID, extension, mimeType string) *commonv1.AssetWriteTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allocate(kind, fileID, extension, mimeType)
}

func (s *Store) allocate(kind, fileID, extension, mimeType string) *commonv1.AssetWriteTarget {
	id := uuid.NewString()
	key, _ := mediaauth.AssetObjectKey(id, extension)
	s.assets[id] = application.Asset{ID: id, SourceFileID: new(fileID), Kind: kind, Status: application.AssetAllocated}
	return &commonv1.AssetWriteTarget{
		AssetId: id, ObjectKey: key, Extension: extension, MimeType: mimeType,
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	}
}

// SeedAllocatedGeneration allocates an HLS generation of fileID.
func (s *Store) SeedAllocatedGeneration(fileID string) *commonv1.MediaGenerationWriteTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := uuid.NewString()
	prefix, _ := mediaauth.MediaHLSObjectPrefix(fileID, id)
	s.generations[id] = Generation{MediaGeneration: application.MediaGeneration{
		ID: id, FileID: fileID, Kind: "hls", Status: application.GenerationAllocated,
	}}
	return &commonv1.MediaGenerationWriteTarget{GenerationId: id, FileId: fileID, ObjectPrefix: prefix}
}

// ReassignGeneration makes a generation belong to another File, as a
// corrupt allocation would.
func (s *Store) ReassignGeneration(generationID, fileID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	generation := s.generations[generationID]
	generation.FileID = fileID
	s.generations[generationID] = generation
}

// SeedJob stores a transcode job.
func (s *Store) SeedJob(job transcode.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.EventID] = job
}

// SeedWaveformJob stores a waveform job.
func (s *Store) SeedWaveformJob(job transcode.WaveformJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waveforms[job.EventID] = job
}

// SeedTrack stores a Track whose audio is sourceFileID.
func (s *Store) SeedTrack(trackID string, track Track) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tracks[trackID] = track
}

// SeedDerivative stores a derivative of a File.
func (s *Store) SeedDerivative(derivative application.Derivative) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.derivatives[derivativeKey{derivative.FileID, derivative.Type}] = derivative
}

// SeedBinding binds an asset to an owner.
func (s *Store) SeedBinding(assetID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings[assetID]++
}

// StoredFile returns a stored File.
func (s *Store) StoredFile(fileID string) File {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.files[fileID]
}

// StoredAsset returns a stored public asset.
func (s *Store) StoredAsset(assetID string) (application.Asset, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, ok := s.assets[assetID]
	return asset, ok
}

// StoredGeneration returns a stored media generation.
func (s *Store) StoredGeneration(generationID string) Generation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generations[generationID]
}

// StoredDerivative returns a File's derivative of type.
func (s *Store) StoredDerivative(fileID, derivativeType string) (application.Derivative, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	derivative, ok := s.derivatives[derivativeKey{fileID, derivativeType}]
	return derivative, ok
}

// StoredTrack returns a stored Track.
func (s *Store) StoredTrack(trackID string) Track {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tracks[trackID]
}

// StoredJob returns a stored transcode job.
func (s *Store) StoredJob(eventID string) (transcode.Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[eventID]
	return job, ok
}

// WaveformJobsOf returns the waveform jobs of a File.
func (s *Store) WaveformJobsOf(fileID string) []transcode.WaveformJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	var jobs []transcode.WaveformJob
	for _, job := range s.waveforms {
		if job.FileID == fileID {
			jobs = append(jobs, job)
		}
	}
	return jobs
}

func (s *Store) FileAcceptsResults(_ context.Context, fileID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, ok := s.files[fileID]
	return ok && !file.DeleteRequested, nil
}

func (s *Store) LockFileAcceptingResults(ctx context.Context, fileID string) (bool, error) {
	return s.FileAcceptsResults(ctx, fileID)
}

func (s *Store) ProcessingFile(_ context.Context, fileID string) (application.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, ok := s.files[fileID]
	if !ok {
		return application.File{}, application.ErrFileNotFound
	}
	return file.File, nil
}

func (s *Store) SetFileDuration(_ context.Context, fileID string, seconds int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if file, ok := s.files[fileID]; ok {
		file.DurationSeconds = new(seconds)
		s.files[fileID] = file
	}
	return nil
}

func (s *Store) WaveformSource(_ context.Context, fileID string) (*commonv1.MediaObjectTarget, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, ok := s.files[fileID]
	if !ok || file.DeleteRequested {
		return nil, false, nil
	}
	key, err := mediaauth.MediaObjectKey(fileID, file.Extension)
	if err != nil {
		return nil, false, fmt.Errorf("build waveform source target: %w", err)
	}
	return &commonv1.MediaObjectTarget{FileId: fileID, ObjectKey: key, Extension: file.Extension, MimeType: file.MimeType}, true, nil
}

func (s *Store) TranscodeJob(_ context.Context, identity transcode.CompletionIdentity, _ bool) (*transcode.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[identity.EventID]
	if !ok || job.QueueName != identity.QueueName || job.EntityType != identity.EntityType ||
		job.EntityID != identity.EntityID || job.FileID != identity.FileID {
		return nil, nil
	}
	return &job, nil
}

func (s *Store) FailActiveTranscodeJob(_ context.Context, eventID, reason string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[eventID]
	if !ok || !slices.Contains(transcode.ActiveStatuses(), job.Status) {
		return false, nil
	}
	job.Status, job.LastError, job.CompletedAt, job.UpdatedAt = transcode.StatusFailedTerminal, new(reason), new(now), now
	s.jobs[eventID] = job
	return true, nil
}

func (s *Store) CancelActiveTranscodeJob(_ context.Context, eventID, reason string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[eventID]
	if ok && slices.Contains(transcode.ActiveStatuses(), job.Status) {
		job.Status, job.LastError, job.CompletedAt, job.UpdatedAt = transcode.StatusCancelled, new(reason), new(now), now
		s.jobs[eventID] = job
	}
	return nil
}

func (s *Store) DeleteTranscodeJob(_ context.Context, eventID, fileID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, ok := s.jobs[eventID]; ok && job.FileID == fileID {
		delete(s.jobs, eventID)
	}
	return nil
}

func (s *Store) FailedTranscodeJobs(_ context.Context, fileID, queueName string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, job := range s.jobs {
		if job.FileID == fileID && job.QueueName == queueName && job.Status == transcode.StatusFailedTerminal {
			count++
		}
	}
	return count, nil
}

func (s *Store) FileTranscodeJobs(_ context.Context, fileID string) ([]transcode.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var jobs []transcode.Job
	for _, job := range s.jobs {
		if job.FileID == fileID {
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}

func (s *Store) SaveWaveformJob(_ context.Context, job transcode.WaveformJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waveforms[job.EventID] = job
	return nil
}

func (s *Store) WaveformJob(_ context.Context, eventID string) (*transcode.WaveformJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.waveforms[eventID]
	if !ok {
		return nil, nil
	}
	return &job, nil
}

func (s *Store) DeleteWaveformJob(_ context.Context, eventID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.waveforms, eventID)
	return nil
}

func (s *Store) DeleteFileWaveformJob(_ context.Context, eventID, fileID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, ok := s.waveforms[eventID]; ok && job.FileID == fileID {
		delete(s.waveforms, eventID)
	}
	return nil
}

func (s *Store) FailWaveformJob(_ context.Context, eventID, reason string, now time.Time) error {
	return s.updateWaveform(eventID, func(job *transcode.WaveformJob) {
		job.Status, job.LastError, job.CompletedAt, job.UpdatedAt = transcode.WaveformJobStatusFailed, new(reason), new(now), now
	})
}

func (s *Store) CancelWaveformJob(_ context.Context, eventID, reason string, now time.Time) error {
	return s.updateWaveform(eventID, func(job *transcode.WaveformJob) {
		job.Status, job.CancelRequested, job.LastError, job.CompletedAt, job.UpdatedAt =
			transcode.WaveformJobStatusCancelled, true, new(reason), new(now), now
	})
}

func (s *Store) RecordWaveformProgress(_ context.Context, eventID string, progress int, _ int64, _ string, now time.Time) error {
	return s.updateWaveform(eventID, func(job *transcode.WaveformJob) {
		if job.Status == transcode.WaveformJobStatusQueued && !job.CancelRequested {
			job.Progress, job.UpdatedAt = max(job.Progress, progress), now
		}
	})
}

func (s *Store) updateWaveform(eventID string, change func(*transcode.WaveformJob)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, ok := s.waveforms[eventID]; ok {
		change(&job)
		s.waveforms[eventID] = job
	}
	return nil
}

func (s *Store) QueuedWaveformJobs(_ context.Context, fileID string) (int64, error) {
	return s.countWaveforms(fileID, transcode.WaveformJobStatusQueued), nil
}

func (s *Store) FailedWaveformJobs(_ context.Context, fileID string) (int64, error) {
	return s.countWaveforms(fileID, transcode.WaveformJobStatusFailed), nil
}

func (s *Store) countWaveforms(fileID, status string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, job := range s.waveforms {
		if job.FileID == fileID && job.Status == status && !job.CancelRequested {
			count++
		}
	}
	return count
}

func (s *Store) FileWaveformJobs(_ context.Context, fileID string) ([]transcode.WaveformJob, error) {
	return s.WaveformJobsOf(fileID), nil
}

func (s *Store) DerivativeAsset(_ context.Context, fileID, derivativeType string) (*string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	derivative, ok := s.derivatives[derivativeKey{fileID, derivativeType}]
	return derivative.AssetID, ok, nil
}

func (s *Store) DerivativeCount(_ context.Context, fileID, derivativeType string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.derivatives[derivativeKey{fileID, derivativeType}]; ok {
		return 1, nil
	}
	return 0, nil
}

func (s *Store) DerivativeTypes(_ context.Context, fileID string, types []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []string
	for key := range s.derivatives {
		if key.fileID == fileID && slices.Contains(types, key.derivativeType) {
			found = append(found, key.derivativeType)
		}
	}
	return found, nil
}

func (s *Store) UpsertDerivative(_ context.Context, derivative application.Derivative) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if derivative.AssetID != nil {
		if _, ok := s.assets[*derivative.AssetID]; !ok {
			return errors.New("derivative asset does not exist")
		}
	}
	s.derivatives[derivativeKey{derivative.FileID, derivative.Type}] = derivative
	return nil
}

func (s *Store) DeleteDerivative(_ context.Context, fileID, derivativeType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.derivatives, derivativeKey{fileID, derivativeType})
	return nil
}

func (s *Store) ReadyDerivatives(_ context.Context, fileID string) ([]application.Derivative, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ready []application.Derivative
	for key, derivative := range s.derivatives {
		if key.fileID != fileID {
			continue
		}
		if derivative.AssetID != nil && s.assets[*derivative.AssetID].Status == application.AssetReady ||
			derivative.MediaGenerationID != nil && s.generations[*derivative.MediaGenerationID].Status == application.GenerationReady {
			ready = append(ready, derivative)
		}
	}
	return ready, nil
}

func (s *Store) AssetDerivativeCount(_ context.Context, assetID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, derivative := range s.derivatives {
		if derivative.AssetID != nil && *derivative.AssetID == assetID {
			count++
		}
	}
	return count, nil
}

func (s *Store) WaveformAssetReferenced(_ context.Context, assetID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, derivative := range s.derivatives {
		if key.derivativeType == "FILE_DERIVATIVE_TYPE_WAVEFORM" && derivative.AssetID != nil && *derivative.AssetID == assetID {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) GenerationDerivativeCount(_ context.Context, generationID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, derivative := range s.derivatives {
		if derivative.MediaGenerationID != nil && *derivative.MediaGenerationID == generationID {
			count++
		}
	}
	return count, nil
}

func (s *Store) CompleteAsset(_ context.Context, result *commonv1.AssetWriteResult) (application.Asset, error) {
	if result == nil {
		return application.Asset{}, domainerr.Required("result")
	}
	if result.GetFileSize() <= 0 {
		return application.Asset{}, domainerr.InvalidField("file_size", "must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, ok := s.assets[result.GetAssetId()]
	if !ok {
		return application.Asset{}, domainerr.ResourceNotFound("public_asset", result.GetAssetId())
	}
	switch asset.Status {
	case application.AssetReady:
		if asset.FileSize == nil || *asset.FileSize != result.GetFileSize() || !bytes.Equal(asset.SHA256, result.GetSha256()) {
			return application.Asset{}, domainerr.Precondition("public asset completion conflicts with ready metadata")
		}
	case application.AssetAllocated:
		asset.Status, asset.FileSize, asset.SHA256 = application.AssetReady, new(result.GetFileSize()), slices.Clone(result.GetSha256())
		s.assets[asset.ID] = asset
	default:
		return application.Asset{}, domainerr.Precondition("public asset is not allocated")
	}
	return asset, nil
}

func (s *Store) RequestAssetDeletion(_ context.Context, assetID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, ok := s.assets[assetID]
	if !ok || s.bindings[assetID] != 0 {
		return nil
	}
	if asset.Status != application.AssetDeleted {
		asset.Status = application.AssetDeletePending
		s.assets[assetID] = asset
	}
	return nil
}

func (s *Store) AssetBindingCount(_ context.Context, assetID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(s.bindings[assetID]), nil
}

func (s *Store) Asset(_ context.Context, assetID string) (application.Asset, bool, error) {
	asset, ok := s.StoredAsset(assetID)
	return asset, ok, nil
}

func (s *Store) LockAsset(ctx context.Context, assetID string) (application.Asset, bool, error) {
	return s.Asset(ctx, assetID)
}

func (s *Store) MarkAssetDeletePending(_ context.Context, assetID string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, ok := s.assets[assetID]
	if ok && slices.Contains([]string{application.AssetAllocated, application.AssetReady, application.AssetFailed}, asset.Status) {
		asset.Status = application.AssetDeletePending
		s.assets[assetID] = asset
	}
	return nil
}

func (s *Store) AllocateWaveformOutput(_ context.Context, fileID string) (*commonv1.AssetWriteTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allocate("waveform", fileID, "json", "application/json"), nil
}

func (s *Store) CompleteMediaGeneration(_ context.Context, result *commonv1.MediaGenerationWriteResult) (application.MediaGeneration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	generation, ok := s.generations[result.GetGenerationId()]
	if !ok {
		return application.MediaGeneration{}, domainerr.ResourceNotFound("media_generation", result.GetGenerationId())
	}
	switch generation.Status {
	case application.GenerationAllocated:
		generation.Status, generation.ManifestSHA256 = application.GenerationReady, slices.Clone(result.GetManifestSha256())
		s.generations[generation.ID] = generation
	case application.GenerationReady:
	default:
		return application.MediaGeneration{}, domainerr.Precondition("media generation is not allocated")
	}
	return generation.MediaGeneration, nil
}

func (s *Store) LockMediaGeneration(_ context.Context, generationID string) (application.MediaGeneration, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	generation, ok := s.generations[generationID]
	return generation.MediaGeneration, ok, nil
}

func (s *Store) RetireMediaGeneration(_ context.Context, generationID string, result *commonv1.MediaGenerationWriteResult, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	generation, ok := s.generations[generationID]
	if !ok || (generation.Status != application.GenerationAllocated && generation.Status != application.GenerationReady) {
		return nil
	}
	generation.Status, generation.DeleteAfter = application.GenerationRetired, new(now.Add(7*time.Hour))
	if result != nil {
		generation.ManifestSHA256 = slices.Clone(result.GetManifestSha256())
	}
	s.generations[generationID] = generation
	return nil
}

func (s *Store) UpdateTrackProcessing(_ context.Context, trackID, fileID, status string, durationSeconds *int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	track, ok := s.tracks[trackID]
	if !ok || track.SourceFileID != fileID {
		return false, nil
	}
	track.Status = status
	if durationSeconds != nil {
		track.DurationSeconds = new(*durationSeconds)
	}
	s.tracks[trackID] = track
	return true, nil
}

func (s *Store) TrackSourceMatches(_ context.Context, trackID, fileID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	track, ok := s.tracks[trackID]
	return ok && track.SourceFileID == fileID, nil
}

func (s *Store) TrackRelease(_ context.Context, trackID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	track, ok := s.tracks[trackID]
	return track.ReleaseID, ok, nil
}

var _ application.Store = (*Store)(nil)
