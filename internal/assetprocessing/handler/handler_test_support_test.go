package handler

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/assetprocessing/optimizer"
	"github.com/echovisionlab/geul-api/internal/assetprocessing/workdir"
)

const (
	testJobID        = "33333333-3333-4333-8333-333333333333"
	testSourceFileID = "11111111-1111-4111-8111-111111111111"
	testOutputFileID = "22222222-2222-4222-8222-222222222222"
	testSourceKey    = "media/11111111-1111-4111-8111-111111111111.glb"
	testOutputKey    = "media/22222222-2222-4222-8222-222222222222.glb"
)

func newTestProcessor(t *testing.T, tempRoot string) (*Processor, *fakeStorage, *fakeOptimizer, *fakePublisher) {
	t.Helper()
	workDirs, err := workdir.NewManager(tempRoot)
	if err != nil {
		t.Fatalf("new workdir manager: %v", err)
	}
	storage := &fakeStorage{
		size:    3,
		payload: []byte("glb"),
		uploads: map[string]uploadRecord{},
		deleted: map[string]bool{},
	}
	meshOptimizer := &fakeOptimizer{}
	publisher := &fakePublisher{}
	processor, err := NewProcessor(Config{MaxInputBytes: 50 * 1024 * 1024}, workDirs, storage, meshOptimizer, publisher)
	if err != nil {
		t.Fatalf("new processor: %v", err)
	}
	return processor, storage, meshOptimizer, publisher
}

func validJob() *apiv1.MeshOptimizationJob {
	return &apiv1.MeshOptimizationJob{
		JobId:         testJobID,
		CorrelationId: "corr-1",
		Identity: &apiv1.MeshOptimizationIdentity{
			EntityType: apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
			EntityId:   "page-1",
			FileId:     testSourceFileID,
			Source: &commonv1.MediaObjectTarget{
				FileId:    testSourceFileID,
				ObjectKey: testSourceKey,
				Extension: "glb",
				MimeType:  "model/gltf-binary",
			},
		},
		Options: &apiv1.MeshOptimizationOptions{
			CompressionMethod:  apiv1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_DRACO,
			TargetRatioPercent: 50,
			Profile:            apiv1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1,
		},
		Output: &commonv1.MediaObjectTarget{
			FileId:    testOutputFileID,
			ObjectKey: testOutputKey,
			Extension: "glb",
			MimeType:  "model/gltf-binary",
		},
	}
}

func mustMarshalJob(t *testing.T, job *apiv1.MeshOptimizationJob) []byte {
	t.Helper()
	body, err := proto.Marshal(job)
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	return body
}

func assertTempRootEmpty(t *testing.T, tempRoot string) {
	t.Helper()
	entries, err := os.ReadDir(tempRoot)
	if err != nil {
		t.Fatalf("read temp root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected temp root cleanup, found %d entries", len(entries))
	}
}

type uploadRecord struct {
	contentType string
	payload     []byte
}

type fakeStorage struct {
	size        int64
	payload     []byte
	downloaded  bool
	uploads     map[string]uploadRecord
	deleted     map[string]bool
	sizeErr     error
	downloadErr error
	uploadErr   error
	deleteErr   error
}

func (s *fakeStorage) GetObjectSize(context.Context, string) (int64, error) {
	return s.size, s.sizeErr
}

func (s *fakeStorage) Download(_ context.Context, _ string, localPath string) error {
	if s.downloadErr != nil {
		return s.downloadErr
	}
	s.downloaded = true
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(localPath, s.payload, 0644)
}

func (s *fakeStorage) Upload(_ context.Context, key string, localPath string, contentType string) error {
	if s.uploadErr != nil {
		return s.uploadErr
	}
	payload, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	s.uploads[key] = uploadRecord{contentType: contentType, payload: payload}
	return nil
}

func (s *fakeStorage) Delete(_ context.Context, key string) error {
	s.deleted[key] = true
	return s.deleteErr
}

type fakeOptimizer struct {
	inspectCalls    []string
	optimizeOptions []optimizer.Options
	optimizeErr     error
	inspectErrAt    int
}

func (o *fakeOptimizer) Inspect(_ context.Context, path string) (optimizer.Inspection, error) {
	o.inspectCalls = append(o.inspectCalls, path)
	if o.inspectErrAt == len(o.inspectCalls) {
		return optimizer.Inspection{}, errors.New("inspect failed")
	}
	if len(o.inspectCalls) == 1 {
		return optimizer.Inspection{UploadVertexCount: 300, GLPrimitiveCount: 120}, nil
	}
	return optimizer.Inspection{UploadVertexCount: 150, GLPrimitiveCount: 45, UsesDracoCompression: true}, nil
}

func (o *fakeOptimizer) Optimize(_ context.Context, _ string, outputPath string, options optimizer.Options) error {
	o.optimizeOptions = append(o.optimizeOptions, options)
	if o.optimizeErr != nil {
		return o.optimizeErr
	}
	return os.WriteFile(outputPath, []byte("optimized-glb"), 0644)
}

type fakePublisher struct {
	progressEvents []*apiv1.MeshOptimizationProgressEvent
	completeEvents []*apiv1.MeshOptimizationCompleteEvent
	failEvents     []*apiv1.MeshOptimizationFailEvent
	completeErr    error
	progressErr    error
	failErr        error
}

func (p *fakePublisher) PublishMeshOptimizationProgress(_ context.Context, event *apiv1.MeshOptimizationProgressEvent) error {
	p.progressEvents = append(p.progressEvents, event)
	return p.progressErr
}

func (p *fakePublisher) PublishMeshOptimizationComplete(_ context.Context, event *apiv1.MeshOptimizationCompleteEvent) error {
	p.completeEvents = append(p.completeEvents, event)
	return p.completeErr
}

func (p *fakePublisher) PublishMeshOptimizationFail(_ context.Context, event *apiv1.MeshOptimizationFailEvent) error {
	p.failEvents = append(p.failEvents, event)
	return p.failErr
}

type fakeWorkDirs struct {
	root       string
	createErr  error
	cleanupErr error
}

func (w *fakeWorkDirs) CreateJobWorkDir(string) (string, error) {
	if w.createErr != nil {
		return "", w.createErr
	}
	return os.MkdirTemp(w.root, "mesh-")
}

func (w *fakeWorkDirs) CleanupJobWorkDir(path string) error {
	if w.cleanupErr != nil {
		return w.cleanupErr
	}
	return os.RemoveAll(path)
}

func resetHandlerOps(t *testing.T) {
	t.Helper()
	statFile = os.Stat
	openFile = os.Open
	copyStream = io.Copy
	buildMediaObjectKey = mediaauth.MediaObjectKey
	t.Cleanup(func() {
		statFile = os.Stat
		openFile = os.Open
		copyStream = io.Copy
		buildMediaObjectKey = mediaauth.MediaObjectKey
	})
}
