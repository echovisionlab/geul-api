package handler

import (
	"fmt"
	"strings"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/assetprocessing/optimizer"
)

type meshJobSpec struct {
	jobID        string
	sourceKey    string
	outputKey    string
	outputTarget *commonv1.MediaObjectTarget
	options      optimizer.Options
}

var buildMediaObjectKey = mediaauth.MediaObjectKey

func newMeshJobSpec(job *apiv1.MeshOptimizationJob) (meshJobSpec, error) {
	if err := validateJob(job); err != nil {
		return meshJobSpec{}, err
	}
	options, err := optimizer.OptionsFromEvent(job.GetOptions())
	if err != nil {
		return meshJobSpec{}, err
	}
	return meshJobSpec{
		jobID:        strings.TrimSpace(job.GetJobId()),
		sourceKey:    job.GetIdentity().GetSource().GetObjectKey(),
		outputKey:    job.GetOutput().GetObjectKey(),
		outputTarget: job.GetOutput(),
		options:      options,
	}, nil
}

func validateJob(job *apiv1.MeshOptimizationJob) error {
	if err := validateJobID(job); err != nil {
		return err
	}
	if strings.TrimSpace(job.GetCorrelationId()) == "" {
		return fmt.Errorf("correlation_id is required")
	}
	identity, err := validateIdentity(job.GetIdentity())
	if err != nil {
		return err
	}
	return validateOutput(job.GetOutput(), identity.GetFileId())
}

func validateIdentity(identity *apiv1.MeshOptimizationIdentity) (*apiv1.MeshOptimizationIdentity, error) {
	if identity == nil {
		return nil, fmt.Errorf("identity is required")
	}
	if identity.GetEntityType() == apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED {
		return nil, fmt.Errorf("identity.entity_type is required")
	}
	if strings.TrimSpace(identity.GetFileId()) == "" {
		return nil, fmt.Errorf("identity.file_id is required")
	}
	if strings.TrimSpace(identity.GetEntityId()) == "" {
		return nil, fmt.Errorf("identity.entity_id is required")
	}
	if err := validateMediaTarget("identity.source", identity.GetSource()); err != nil {
		return nil, err
	}
	if identity.GetSource().GetFileId() != identity.GetFileId() {
		return nil, fmt.Errorf("identity.source.file_id must match identity.file_id")
	}
	return identity, nil
}

func validateOutput(output *commonv1.MediaObjectTarget, sourceFileID string) error {
	if err := validateMediaTarget("output", output); err != nil {
		return err
	}
	if output.GetFileId() == sourceFileID {
		return fmt.Errorf("output.file_id must differ from identity.file_id")
	}
	return nil
}

func validateJobID(job *apiv1.MeshOptimizationJob) error {
	if strings.TrimSpace(job.GetJobId()) == "" {
		return fmt.Errorf("job_id is required")
	}
	if _, err := uuid.Parse(job.GetJobId()); err != nil {
		return fmt.Errorf("job_id must be a UUID")
	}
	return nil
}

func validateMediaTarget(field string, target *commonv1.MediaObjectTarget) error {
	if target == nil {
		return fmt.Errorf("%s is required", field)
	}
	if _, err := uuid.Parse(target.GetFileId()); err != nil {
		return fmt.Errorf("%s.file_id must be a UUID", field)
	}
	if target.GetExtension() != "glb" {
		return fmt.Errorf("%s.extension must be glb", field)
	}
	if target.GetMimeType() != "model/gltf-binary" {
		return fmt.Errorf("%s.mime_type must be model/gltf-binary", field)
	}
	expectedKey, err := buildMediaObjectKey(target.GetFileId(), target.GetExtension())
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	if target.GetObjectKey() != expectedKey {
		return fmt.Errorf("%s.object_key must equal %s", field, expectedKey)
	}
	return nil
}

func cloneIdentity(identity *apiv1.MeshOptimizationIdentity) *apiv1.MeshOptimizationIdentity {
	if identity == nil {
		return nil
	}
	return proto.Clone(identity).(*apiv1.MeshOptimizationIdentity)
}
