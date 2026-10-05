package handler

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/proto"
)

func TestValidateJob(t *testing.T) {
	resetHandlerOps(t)
	if err := validateJob(validJob()); err != nil {
		t.Fatalf("valid job: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*apiv1.MeshOptimizationJob)
		want   string
	}{
		{name: "job id required", mutate: func(job *apiv1.MeshOptimizationJob) { job.JobId = " " }, want: "job_id is required"},
		{name: "job id uuid", mutate: func(job *apiv1.MeshOptimizationJob) { job.JobId = "job" }, want: "job_id must be a UUID"},
		{name: "correlation", mutate: func(job *apiv1.MeshOptimizationJob) { job.CorrelationId = "" }, want: "correlation_id"},
		{name: "identity", mutate: func(job *apiv1.MeshOptimizationJob) { job.Identity = nil }, want: "identity is required"},
		{name: "entity type", mutate: func(job *apiv1.MeshOptimizationJob) { job.Identity.EntityType = 0 }, want: "entity_type"},
		{name: "file id", mutate: func(job *apiv1.MeshOptimizationJob) { job.Identity.FileId = "" }, want: "identity.file_id"},
		{name: "entity id", mutate: func(job *apiv1.MeshOptimizationJob) { job.Identity.EntityId = "" }, want: "identity.entity_id"},
		{name: "source target", mutate: func(job *apiv1.MeshOptimizationJob) { job.Identity.Source = nil }, want: "identity.source is required"},
		{name: "source mismatch", mutate: func(job *apiv1.MeshOptimizationJob) { job.Identity.FileId = testOutputFileID }, want: "must match"},
		{name: "output target", mutate: func(job *apiv1.MeshOptimizationJob) { job.Output = nil }, want: "output is required"},
		{name: "same output", mutate: func(job *apiv1.MeshOptimizationJob) {
			job.Output = proto.Clone(job.Identity.Source).(*commonv1.MediaObjectTarget)
		}, want: "must differ"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := validJob()
			test.mutate(job)
			err := validateJob(job)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}
}

func TestValidateMediaTarget(t *testing.T) {
	resetHandlerOps(t)
	valid := validJob().GetOutput()
	if err := validateMediaTarget("output", valid); err != nil {
		t.Fatalf("valid target: %v", err)
	}

	tests := []struct {
		name   string
		target func() *commonv1.MediaObjectTarget
		want   string
	}{
		{name: "nil", target: func() *commonv1.MediaObjectTarget { return nil }, want: "is required"},
		{name: "uuid", target: func() *commonv1.MediaObjectTarget {
			target := proto.Clone(valid).(*commonv1.MediaObjectTarget)
			target.FileId = "not-a-uuid"
			return target
		}, want: "must be a UUID"},
		{name: "extension", target: func() *commonv1.MediaObjectTarget {
			target := proto.Clone(valid).(*commonv1.MediaObjectTarget)
			target.Extension = "obj"
			return target
		}, want: "extension must be glb"},
		{name: "mime", target: func() *commonv1.MediaObjectTarget {
			target := proto.Clone(valid).(*commonv1.MediaObjectTarget)
			target.MimeType = "application/octet-stream"
			return target
		}, want: "mime_type"},
		{name: "key", target: func() *commonv1.MediaObjectTarget {
			target := proto.Clone(valid).(*commonv1.MediaObjectTarget)
			target.ObjectKey = "media/arbitrary.glb"
			return target
		}, want: "object_key"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateMediaTarget("output", test.target())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}

	buildMediaObjectKey = func(string, string) (string, error) { return "", errors.New("canonical key failed") }
	if err := validateMediaTarget("output", valid); err == nil || !strings.Contains(err.Error(), "canonical key failed") {
		t.Fatalf("expected canonical key error, got %v", err)
	}
}

func TestFileSHA256AndCloneIdentity(t *testing.T) {
	resetHandlerOps(t)
	if _, err := fileSHA256(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected open error")
	}
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("payload"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	copyStream = func(io.Writer, io.Reader) (int64, error) { return 0, errors.New("copy failed") }
	if _, err := fileSHA256(path); err == nil {
		t.Fatal("expected copy error")
	}
	copyStream = io.Copy
	got, err := fileSHA256(path)
	if err != nil {
		t.Fatalf("hash file: %v", err)
	}
	want := sha256.Sum256([]byte("payload"))
	if string(got) != string(want[:]) {
		t.Fatal("unexpected SHA-256")
	}
	if cloneIdentity(nil) != nil {
		t.Fatal("nil identity should stay nil")
	}
	original := validJob().Identity
	cloned := cloneIdentity(original)
	if cloned == original || !proto.Equal(cloned, original) {
		t.Fatal("identity must be deep-cloned")
	}

	publisher := &fakePublisher{failErr: errors.New("publish failed")}
	processor := &Processor{publisher: publisher}
	cause := errors.New("cause")
	err = processor.publishFail(context.Background(), validJob(), time.Now(), apiv1.MeshOptimizationFailureReason_MESH_OPTIMIZATION_FAILURE_REASON_INTERNAL, cause)
	if err == nil || !strings.Contains(err.Error(), "additionally failed") {
		t.Fatalf("expected combined publication error, got %v", err)
	}
}
