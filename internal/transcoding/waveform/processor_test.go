package waveform

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/transcoding/jobresult"
	"github.com/echovisionlab/geul-api/internal/transcoding/mq"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type fakeWorkDirManager struct {
	baseDir       string
	createdJobIDs []string
	cleanedJobIDs []string
	createErr     error
	cleanupErr    error
	returnPath    string
}

func (f *fakeWorkDirManager) CreateJobWorkDir(jobID string) (string, error) {
	f.createdJobIDs = append(f.createdJobIDs, jobID)
	if f.createErr != nil {
		return "", f.createErr
	}
	if f.returnPath != "" {
		return f.returnPath, nil
	}
	dir := filepath.Join(f.baseDir, jobID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func (f *fakeWorkDirManager) CleanupJobWorkDir(jobID string) error {
	f.cleanedJobIDs = append(f.cleanedJobIDs, jobID)
	if f.cleanupErr != nil {
		return f.cleanupErr
	}
	return os.RemoveAll(filepath.Join(f.baseDir, jobID))
}

type fakePeakGenerator struct {
	peaks           [][]float64
	err             error
	lastExtractPath string
	blockUntilCtx   bool
}

func (f *fakePeakGenerator) GeneratePeaks(ctx context.Context, inputPath string) ([][]float64, error) {
	f.lastExtractPath = inputPath
	if f.blockUntilCtx {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	peaks := make([][]float64, 0, len(f.peaks))
	for _, channel := range f.peaks {
		peaks = append(peaks, append([]float64(nil), channel...))
	}
	return peaks, nil
}

type uploadedObject struct {
	key         string
	contentType string
	payload     []byte
}

type fakeStorageClient struct {
	downloads         []string
	uploads           []uploadedObject
	downloadErr       error
	uploadErr         error
	skipDownloadWrite bool
	uploadHook        func()
	completions       map[string][]byte
	completionErr     error
}

func (f *fakeStorageClient) Download(_ context.Context, key string, localPath string) error {
	f.downloads = append(f.downloads, key)
	if f.downloadErr != nil {
		return f.downloadErr
	}
	if f.skipDownloadWrite {
		return nil
	}
	return os.WriteFile(localPath, []byte("audio-bytes"), 0o644)
}

func (f *fakeStorageClient) Upload(
	_ context.Context,
	key string,
	localPath string,
	contentType string,
) error {
	if f.uploadHook != nil {
		f.uploadHook()
	}
	if f.uploadErr != nil {
		return f.uploadErr
	}
	payload, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	f.uploads = append(f.uploads, uploadedObject{
		key:         key,
		contentType: contentType,
		payload:     payload,
	})
	return nil
}

func (f *fakeStorageClient) UploadCompleted(ctx context.Context, key, localPath, contentType string, completion []byte) error {
	if err := f.Upload(ctx, key, localPath, contentType); err != nil {
		return err
	}
	if f.completions == nil {
		f.completions = make(map[string][]byte)
	}
	f.completions[key] = append([]byte(nil), completion...)
	return nil
}

func (f *fakeStorageClient) Completion(_ context.Context, key string) ([]byte, bool, error) {
	if f.completionErr != nil {
		return nil, false, f.completionErr
	}
	payload, found := f.completions[key]
	return append([]byte(nil), payload...), found, nil
}

type fakeEventPublisher struct {
	progressEvents []*apiv1.WaveformProgressEvent
	completeEvents []*apiv1.WaveformCompleteEvent
	failEvents     []*apiv1.WaveformFailEvent
	progressErr    error
	completeErr    error
	failErr        error
}

func (f *fakeEventPublisher) PublishWaveformProgress(
	_ context.Context,
	event *apiv1.WaveformProgressEvent,
) error {
	f.progressEvents = append(f.progressEvents, event)
	return f.progressErr
}

func (f *fakeEventPublisher) PublishWaveformComplete(
	_ context.Context,
	event *apiv1.WaveformCompleteEvent,
) error {
	f.completeEvents = append(f.completeEvents, event)
	return f.completeErr
}

func (f *fakeEventPublisher) PublishWaveformFail(
	_ context.Context,
	event *apiv1.WaveformFailEvent,
) error {
	f.failEvents = append(f.failEvents, event)
	return f.failErr
}

func waveformGenerateEvent(eventID, entityID, fileID string) *apiv1.WaveformGenerateEvent {
	eventID = waveformUUID(eventID)
	entityID = waveformUUID(entityID)
	fileID = waveformUUID(fileID)
	assetID := waveformUUID("asset-" + fileID)
	return &apiv1.WaveformGenerateEvent{
		EventId:    eventID,
		EntityType: apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
		EntityId:   entityID,
		FileId:     fileID,
		Source: &commonv1.MediaObjectTarget{
			FileId:    fileID,
			ObjectKey: "media/" + fileID + ".mp3",
			Extension: "mp3",
			MimeType:  "audio/mpeg",
		},
		Output: &commonv1.AssetWriteTarget{
			AssetId:     assetID,
			ObjectKey:   "asset/" + assetID + ".json",
			Extension:   "json",
			MimeType:    "application/json",
			Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
		},
	}
}

func waveformUUID(value string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(value)).String()
}

func TestProcessorHandleGenerateJobUploadsSidecarAndPublishesComplete(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	workDirs := &fakeWorkDirManager{baseDir: tmpDir}
	generator := &fakePeakGenerator{
		peaks: [][]float64{{0.1, 0.5, 0.9}, {0.2, 0.6, 0.4}},
	}
	storageClient := &fakeStorageClient{}
	publisher := &fakeEventPublisher{}
	processor := newTestProcessor(t, workDirs, generator, storageClient, publisher)

	event := waveformGenerateEvent(
		"waveform-job-1",
		"release-track-1",
		"file-1",
	)
	expectedKey := event.GetOutput().GetObjectKey()
	body, err := proto.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	if err := handleWaveformMessage(context.Background(), processor, body); err != nil {
		t.Fatalf("HandleGenerateJob returned error: %v", err)
	}

	assertWaveformUpload(t, storageClient, event, expectedKey)
	assertWaveformCompletion(t, publisher, event)
}

func assertWaveformUpload(t *testing.T, storage *fakeStorageClient, event *apiv1.WaveformGenerateEvent, expectedKey string) {
	t.Helper()
	if len(storage.downloads) != 1 || storage.downloads[0] != event.GetSource().GetObjectKey() {
		t.Fatalf("unexpected downloads: %#v", storage.downloads)
	}
	if len(storage.uploads) != 1 {
		t.Fatalf("expected 1 upload, got %d", len(storage.uploads))
	}
	uploaded := storage.uploads[0]
	if uploaded.key != expectedKey || uploaded.contentType != "application/json" {
		t.Fatalf("unexpected upload metadata: %#v", uploaded)
	}
	if string(uploaded.payload) != `[[0.1,0.5,0.9],[0.2,0.6,0.4]]` {
		t.Fatalf("unexpected upload payload: %s", string(uploaded.payload))
	}
}

func assertWaveformCompletion(t *testing.T, publisher *fakeEventPublisher, event *apiv1.WaveformGenerateEvent) {
	t.Helper()
	if len(publisher.completeEvents) != 1 || len(publisher.progressEvents) != 6 {
		t.Fatalf("unexpected completion events: complete=%d progress=%d", len(publisher.completeEvents), len(publisher.progressEvents))
	}
	if publisher.progressEvents[0].Progress != 5 || publisher.progressEvents[len(publisher.progressEvents)-1].Progress != 100 {
		t.Fatalf("unexpected progress event values: first=%d last=%d", publisher.progressEvents[0].Progress, publisher.progressEvents[len(publisher.progressEvents)-1].Progress)
	}
	if publisher.completeEvents[0].GetOutput().GetAssetId() != event.GetOutput().GetAssetId() {
		t.Fatalf("unexpected waveform asset on complete event: %q", publisher.completeEvents[0].GetOutput().GetAssetId())
	}
	if len(publisher.failEvents) != 0 {
		t.Fatalf("expected no fail events, got %d", len(publisher.failEvents))
	}
}

func TestProcessorHandleGenerateJobPreservesUploadedSidecarWhenCompletePublishIsUncertain(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	workDirs := &fakeWorkDirManager{baseDir: tmpDir}
	generator := &fakePeakGenerator{
		peaks: [][]float64{{0.2, 0.4}},
	}
	storageClient := &fakeStorageClient{}
	publisher := &fakeEventPublisher{
		completeErr: errors.New("publish complete failed"),
	}
	processor := newTestProcessor(t, workDirs, generator, storageClient, publisher)

	event := waveformGenerateEvent(
		"waveform-job-2",
		"release-track-2",
		"file-2",
	)
	body, err := proto.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	err = handleWaveformMessage(context.Background(), processor, body)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !jobresult.IsRetry(err) {
		t.Fatalf("expected retryable completion publish error, got %v", err)
	}

	if len(storageClient.uploads) != 1 {
		t.Fatalf("expected completion uncertainty to preserve one uploaded waveform, got %#v", storageClient.uploads)
	}
	if len(publisher.failEvents) != 0 {
		t.Fatalf("completion uncertainty must not be converted to failure, got %d fail events", len(publisher.failEvents))
	}
}

func TestProcessorFreshDuplicateReplaysAcceptedCompletionWithoutTouchingOutput(t *testing.T) {
	t.Parallel()
	storageClient := &fakeStorageClient{}
	publisher := &fakeEventPublisher{completeErr: errors.New("confirmation uncertain")}
	event := waveformGenerateEvent("fresh-duplicate", "entity", "file")
	body := marshalWaveformEvent(t, event)

	first := newTestProcessor(t,
		&fakeWorkDirManager{baseDir: t.TempDir()},
		&fakePeakGenerator{peaks: [][]float64{{0.1, 0.2}}},
		storageClient,
		publisher,
	)
	err := handleWaveformMessage(context.Background(), first, body)
	require.Error(t, err)
	require.True(t, jobresult.IsRetry(err))
	require.Len(t, storageClient.uploads, 1)
	acceptedUploads := append([]uploadedObject(nil), storageClient.uploads...)
	acceptedDownloads := append([]string(nil), storageClient.downloads...)

	publisher.completeErr = nil
	fresh := newTestProcessor(t,
		&fakeWorkDirManager{baseDir: t.TempDir(), createErr: errors.New("duplicate must not reprocess")},
		&fakePeakGenerator{err: errors.New("duplicate must not regenerate")},
		storageClient,
		publisher,
	)
	require.NoError(t, handleWaveformMessage(context.Background(), fresh, body))
	require.Equal(t, acceptedUploads, storageClient.uploads)
	require.Equal(t, acceptedDownloads, storageClient.downloads)
	require.Len(t, publisher.completeEvents, 2)
	require.Equal(t, event.GetEventId(), publisher.completeEvents[1].GetEventId())
}

func TestProcessorCompletionReplayRejectsUnavailableOrInvalidReceipts(t *testing.T) {
	t.Parallel()
	event := waveformGenerateEvent("receipt-boundary", "entity", "file")
	key := event.GetOutput().GetObjectKey()
	storage := &fakeStorageClient{completionErr: errors.New("head unavailable")}
	publisher := &fakeEventPublisher{}
	processor := newTestProcessor(t, &fakeWorkDirManager{baseDir: t.TempDir()}, &fakePeakGenerator{}, storage, publisher)

	replayed, err := processor.replayCompletion(context.Background(), event, key)
	require.False(t, replayed)
	require.True(t, jobresult.IsRetry(err))

	storage.completionErr = nil
	storage.completions = map[string][]byte{key: []byte("invalid")}
	replayed, err = processor.replayCompletion(context.Background(), event, key)
	require.False(t, replayed)
	require.ErrorContains(t, err, "decode waveform completion")

	mismatch, marshalErr := proto.Marshal(&apiv1.WaveformCompleteEvent{EventId: waveformUUID("other")})
	require.NoError(t, marshalErr)
	storage.completions[key] = mismatch
	replayed, err = processor.replayCompletion(context.Background(), event, key)
	require.False(t, replayed)
	require.ErrorContains(t, err, "does not match command")

	valid, marshalErr := proto.Marshal(&apiv1.WaveformCompleteEvent{
		EventId:    event.GetEventId(),
		EntityType: event.GetEntityType(),
		EntityId:   event.GetEntityId(),
		FileId:     event.GetFileId(),
		Output:     &commonv1.AssetWriteResult{AssetId: event.GetOutput().GetAssetId()},
	})
	require.NoError(t, marshalErr)
	storage.completions[key] = valid
	publisher.completeErr = errors.New("result unavailable")
	replayed, err = processor.replayCompletion(context.Background(), event, key)
	require.False(t, replayed)
	require.True(t, jobresult.IsRetry(err))
}

func TestProcessorRetriesWhenCompletionLookupIsUnavailable(t *testing.T) {
	t.Parallel()
	storage := &fakeStorageClient{completionErr: errors.New("head unavailable")}
	processor := newTestProcessor(t,
		&fakeWorkDirManager{baseDir: t.TempDir(), createErr: errors.New("must not process")},
		&fakePeakGenerator{},
		storage,
		&fakeEventPublisher{},
	)
	event := waveformGenerateEvent("lookup-unavailable", "entity", "file")
	err := handleWaveformMessage(context.Background(), processor, marshalWaveformEvent(t, event))
	require.True(t, jobresult.IsRetry(err))
}

func TestProcessorHandleGenerateJobStopsQuietlyWhenCancelled(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	workDirs := &fakeWorkDirManager{baseDir: tmpDir}
	generator := &fakePeakGenerator{blockUntilCtx: true}
	storageClient := &fakeStorageClient{}
	publisher := &fakeEventPublisher{}
	processor := newTestProcessor(t, workDirs, generator, storageClient, publisher)

	event := waveformGenerateEvent(
		"waveform-job-cancel",
		"release-track-cancel",
		"file-cancel",
	)
	body, err := proto.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- handleWaveformMessage(context.Background(), processor, body)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if processor.CancelJob(event.GetEventId()) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for waveform job registration")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := <-done; err != nil {
		t.Fatalf("expected nil error on cancel, got %v", err)
	}
	if len(storageClient.uploads) != 0 {
		t.Fatalf("expected no uploads after cancellation, got %d", len(storageClient.uploads))
	}
	if len(publisher.completeEvents) != 0 || len(publisher.failEvents) != 0 {
		t.Fatalf("expected no terminal events after cancellation, got complete=%d fail=%d", len(publisher.completeEvents), len(publisher.failEvents))
	}
}

func TestNewProcessorRequiresEveryDependency(t *testing.T) {
	t.Parallel()
	workDirs := &fakeWorkDirManager{baseDir: t.TempDir()}
	generator := &fakePeakGenerator{}
	storage := &fakeStorageClient{}
	publisher := &fakeEventPublisher{}
	for _, options := range []Options{
		{Generator: generator, Storage: storage, Publisher: publisher},
		{WorkDirs: workDirs, Storage: storage, Publisher: publisher},
		{WorkDirs: workDirs, Generator: generator, Publisher: publisher},
		{WorkDirs: workDirs, Generator: generator, Storage: storage},
	} {
		processor, err := NewProcessor(options)
		require.Error(t, err)
		require.Nil(t, processor)
	}
}

func TestProcessorRejectsMalformedAndNonCanonicalJobs(t *testing.T) {
	t.Parallel()
	processor := newTestProcessor(t,
		&fakeWorkDirManager{baseDir: t.TempDir()},
		&fakePeakGenerator{},
		&fakeStorageClient{},
		&fakeEventPublisher{},
	)
	require.ErrorContains(t, handleWaveformMessage(context.Background(), processor, []byte("invalid")), "failed to parse waveform job")

	event := waveformGenerateEvent("invalid-contract", "entity", "file")
	event.Output.ObjectKey = "asset/wrong.json"
	require.ErrorContains(t, handleWaveformMessage(context.Background(), processor, marshalWaveformEvent(t, event)), "object_key must be")
}

func TestProcessorFailureBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		workDirs  func(*testing.T) *fakeWorkDirManager
		generator *fakePeakGenerator
		storage   *fakeStorageClient
		publisher *fakeEventPublisher
		want      string
	}{
		{name: "create work dir", workDirs: func(t *testing.T) *fakeWorkDirManager {
			return &fakeWorkDirManager{baseDir: t.TempDir(), createErr: errors.New("mkdir")}
		}, want: "create work dir"},
		{name: "download", storage: &fakeStorageClient{downloadErr: errors.New("download")}, want: "download source asset"},
		{name: "peaks", generator: &fakePeakGenerator{err: errors.New("peaks")}, want: "extract waveform"},
		{name: "empty peaks", generator: &fakePeakGenerator{peaks: [][]float64{}}, want: "empty peaks"},
		{name: "empty channel", generator: &fakePeakGenerator{peaks: [][]float64{{}}}, want: "empty peaks"},
		{name: "marshal", generator: &fakePeakGenerator{peaks: [][]float64{{math.NaN()}}}, want: "marshal waveform"},
		{name: "upload", storage: &fakeStorageClient{uploadErr: errors.New("upload")}, want: "upload waveform asset"},
		{name: "failure publish", generator: &fakePeakGenerator{err: errors.New("peaks")}, publisher: &fakeEventPublisher{failErr: errors.New("publish fail")}, want: "publish waveform failure result"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workDirs := &fakeWorkDirManager{baseDir: t.TempDir()}
			if tc.workDirs != nil {
				workDirs = tc.workDirs(t)
			}
			generator := tc.generator
			if generator == nil {
				generator = &fakePeakGenerator{peaks: [][]float64{{0.1}}}
			}
			storage := tc.storage
			if storage == nil {
				storage = &fakeStorageClient{}
			}
			publisher := tc.publisher
			if publisher == nil {
				publisher = &fakeEventPublisher{}
			}
			processor := newTestProcessor(t, workDirs, generator, storage, publisher)
			event := waveformGenerateEvent("failure-"+tc.name, "entity", "file-"+tc.name)
			err := handleWaveformMessage(context.Background(), processor, marshalWaveformEvent(t, event))
			require.ErrorContains(t, err, tc.want)
			require.Equal(t, tc.name == "failure publish", jobresult.IsRetry(err))
		})
	}
}

func TestProcessorCancellationAndCleanupBoundaries(t *testing.T) {
	t.Parallel()
	t.Run("cancelled download", func(t *testing.T) {
		processor := newTestProcessor(t,
			&fakeWorkDirManager{baseDir: t.TempDir()},
			&fakePeakGenerator{peaks: [][]float64{{0.1}}},
			&fakeStorageClient{downloadErr: context.Canceled},
			&fakeEventPublisher{},
		)
		event := waveformGenerateEvent("cancel-download", "entity", "file")
		require.NoError(t, handleWaveformMessage(context.Background(), processor, marshalWaveformEvent(t, event)))
	})

	t.Run("cancelled upload", func(t *testing.T) {
		processor := newTestProcessor(t,
			&fakeWorkDirManager{baseDir: t.TempDir()},
			&fakePeakGenerator{peaks: [][]float64{{0.1}}},
			&fakeStorageClient{uploadErr: context.Canceled},
			&fakeEventPublisher{},
		)
		event := waveformGenerateEvent("cancel-upload", "entity", "file")
		require.NoError(t, handleWaveformMessage(context.Background(), processor, marshalWaveformEvent(t, event)))
	})

	t.Run("cancel after upload preserves output", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		storage := &fakeStorageClient{uploadHook: cancel}
		processor := newTestProcessor(t,
			&fakeWorkDirManager{baseDir: t.TempDir()},
			&fakePeakGenerator{peaks: [][]float64{{0.1}}},
			storage,
			&fakeEventPublisher{},
		)
		event := waveformGenerateEvent("cancel-after-upload", "entity", "file")
		require.NoError(t, handleWaveformMessage(ctx, processor, marshalWaveformEvent(t, event)))
		require.Len(t, storage.uploads, 1)
	})
}

func TestProcessorIgnoresConcurrentDuplicateEvent(t *testing.T) {
	t.Parallel()
	processor := newTestProcessor(t,
		&fakeWorkDirManager{baseDir: t.TempDir()},
		&fakePeakGenerator{peaks: [][]float64{{0.1}}},
		&fakeStorageClient{},
		&fakeEventPublisher{},
	)
	event := waveformGenerateEvent("duplicate", "entity", "file")
	session, started := processor.jobs.Start(context.Background(), event.GetEventId(), event.GetEventId())
	require.True(t, started)
	duplicate, started := processor.jobs.Start(context.Background(), event.GetEventId(), event.GetEventId())
	require.False(t, started)
	require.Nil(t, duplicate)
	require.NoError(t, handleWaveformMessage(context.Background(), processor, marshalWaveformEvent(t, event)))
	require.True(t, processor.CancelJob(event.GetEventId()))
	require.True(t, processor.CancelJob(event.GetEventId()))
	session.Close()
	require.False(t, processor.CancelJob(event.GetEventId()))
}

func TestProcessorCoversWriteCleanupAndPublisherErrors(t *testing.T) {
	t.Parallel()
	t.Run("write", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "regular-file")
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
		processor := newTestProcessor(t,
			&fakeWorkDirManager{returnPath: path},
			&fakePeakGenerator{peaks: [][]float64{{0.1}}},
			&fakeStorageClient{skipDownloadWrite: true},
			&fakeEventPublisher{},
		)
		event := waveformGenerateEvent("write-error", "entity", "file")
		require.ErrorContains(t, handleWaveformMessage(context.Background(), processor, marshalWaveformEvent(t, event)), "write waveform file")
	})

	t.Run("work dir cleanup", func(t *testing.T) {
		processor := newTestProcessor(t,
			&fakeWorkDirManager{baseDir: t.TempDir(), cleanupErr: errors.New("cleanup")},
			&fakePeakGenerator{peaks: [][]float64{{0.1}}},
			&fakeStorageClient{},
			&fakeEventPublisher{progressErr: errors.New("progress")},
		)
		event := waveformGenerateEvent("cleanup-error", "entity", "file")
		require.NoError(t, handleWaveformMessage(context.Background(), processor, marshalWaveformEvent(t, event)))
	})

	t.Run("completion uncertainty preserves output", func(t *testing.T) {
		storage := &fakeStorageClient{}
		processor := newTestProcessor(t,
			&fakeWorkDirManager{baseDir: t.TempDir()},
			&fakePeakGenerator{peaks: [][]float64{{0.1}}},
			storage,
			&fakeEventPublisher{completeErr: errors.New("complete")},
		)
		event := waveformGenerateEvent("completion-cleanup", "entity", "file")
		err := handleWaveformMessage(context.Background(), processor, marshalWaveformEvent(t, event))
		require.ErrorContains(t, err, "publish waveform complete")
		require.True(t, jobresult.IsRetry(err))
		require.Len(t, storage.uploads, 1)
	})
}

func TestValidateWaveformEventRejectsEveryInvalidBoundary(t *testing.T) {
	t.Parallel()
	require.ErrorContains(t, validateWaveformEvent(nil), "waveform job is required")
	tests := []struct {
		name   string
		mutate func(*apiv1.WaveformGenerateEvent)
		want   string
	}{
		{name: "event id", mutate: func(event *apiv1.WaveformGenerateEvent) { event.EventId = "bad" }, want: "event_id must be a canonical UUID"},
		{name: "uppercase event id", mutate: func(event *apiv1.WaveformGenerateEvent) { event.EventId = strings.ToUpper(event.EventId) }, want: "event_id must be a canonical UUID"},
		{name: "entity id", mutate: func(event *apiv1.WaveformGenerateEvent) { event.EntityId = "bad" }, want: "entity_id must be a canonical UUID"},
		{name: "file id", mutate: func(event *apiv1.WaveformGenerateEvent) { event.FileId = "bad" }, want: "file_id must be a canonical UUID"},
		{name: "entity type", mutate: func(event *apiv1.WaveformGenerateEvent) {
			event.EntityType = apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED
		}, want: "unsupported entity_type"},
		{name: "source missing", mutate: func(event *apiv1.WaveformGenerateEvent) { event.Source = nil }, want: "target is required"},
		{name: "source mismatch", mutate: func(event *apiv1.WaveformGenerateEvent) { event.Source.FileId = waveformUUID("other") }, want: "does not match"},
		{name: "source extension", mutate: func(event *apiv1.WaveformGenerateEvent) { event.Source.Extension = ".mp3" }, want: "extension is not canonical"},
		{name: "source key", mutate: func(event *apiv1.WaveformGenerateEvent) { event.Source.ObjectKey = "media/wrong.mp3" }, want: "object_key must be"},
		{name: "source mime", mutate: func(event *apiv1.WaveformGenerateEvent) { event.Source.MimeType = "x" }, want: "mime_type"},
		{name: "source MIME extension mismatch", mutate: func(event *apiv1.WaveformGenerateEvent) { event.Source.MimeType = "audio/ogg" }, want: "extension does not match"},
		{name: "output missing", mutate: func(event *apiv1.WaveformGenerateEvent) { event.Output = nil }, want: "target is required"},
		{name: "output extension", mutate: func(event *apiv1.WaveformGenerateEvent) { event.Output.Extension = "txt" }, want: "extension must be"},
		{name: "output asset", mutate: func(event *apiv1.WaveformGenerateEvent) { event.Output.AssetId = "bad" }, want: "invalid media path"},
		{name: "output key", mutate: func(event *apiv1.WaveformGenerateEvent) { event.Output.ObjectKey = "asset/wrong.json" }, want: "object_key must be"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			event := waveformGenerateEvent("validate-"+tc.name, "entity", "file")
			tc.mutate(event)
			require.ErrorContains(t, validateWaveformEvent(event), tc.want)
		})
	}
}

func TestWaveformCompletionEncodingFailure(t *testing.T) {
	t.Parallel()
	processor := newTestProcessor(
		t,
		&fakeWorkDirManager{baseDir: t.TempDir()},
		&fakePeakGenerator{},
		&fakeStorageClient{},
		&fakeEventPublisher{},
	)
	event := waveformGenerateEvent("encoding", "entity", "file")
	event.EventId = "\xff"
	_, err := processor.uploadWaveform(context.Background(), event, "unused", []byte("[]"), time.Now())
	require.ErrorContains(t, err, "encode waveform completion")
}

func TestProgressMilestoneRejectsUnknownValue(t *testing.T) {
	t.Parallel()
	require.Panics(t, func() { progressMilestone(255).step() })
}

func marshalWaveformEvent(t *testing.T, event *apiv1.WaveformGenerateEvent) []byte {
	t.Helper()
	body, err := proto.Marshal(event)
	require.NoError(t, err)
	return body
}

func handleWaveformMessage(ctx context.Context, processor *Processor, body []byte) error {
	return mq.DecodeWaveformJob(processor.HandleGenerateJob)(ctx, body)
}

func newTestProcessor(
	t *testing.T,
	workDirs WorkDirManager,
	generator PeakGenerator,
	storage StorageClient,
	publisher EventPublisher,
) *Processor {
	t.Helper()
	processor, err := NewProcessor(Options{
		WorkDirs:  workDirs,
		Generator: generator,
		Storage:   storage,
		Publisher: publisher,
	})
	require.NoError(t, err)
	return processor
}
