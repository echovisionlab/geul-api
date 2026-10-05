package filemedia

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	maxClientMediaArtifactSize  int64 = 64 << 20
	maxClientMediaArtifacts           = 20000
	maxClientMediaTotalSize     int64 = 16 << 30
	clientMediaBufferSize             = 64 << 10
	clientMediaPlanVersion            = 1
	missingClientArtifactReason       = "CLIENT_MEDIA_ARTIFACTS_MISSING:"
)

const (
	clientHLS         = managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS
	clientSpectrogram = managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM
	clientWaveform    = managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM
	clientThumbnail   = managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL
)

var clientMediaPathPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type clientMediaArtifact struct {
	Path           string `json:"path"`
	MimeType       string `json:"mime_type"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256"`
	DerivativeType int32  `json:"derivative_type"`
}
type clientMediaPlan struct {
	MemberID        string                `json:"member_id"`
	Version         int                   `json:"version"`
	Kind            string                `json:"kind"`
	DurationSeconds float64               `json:"duration_seconds"`
	Artifacts       []clientMediaArtifact `json:"artifacts"`
}

func validateClientMediaPlan(plan clientMediaPlan, session model.UploadSession) error {
	if !IsValidUUID(plan.MemberID) {
		return fmt.Errorf("invalid bundle member")
	}
	if plan.Version != clientMediaPlanVersion || (plan.Kind != "audio" && plan.Kind != "video") || !strings.HasPrefix(session.RequestedMime, plan.Kind+"/") {
		return fmt.Errorf("client media kind does not match original")
	}
	uploadType := managev1.UploadType(managev1.UploadType_value[session.UploadType])
	if !supportsClientMedia(uploadType) {
		return fmt.Errorf("upload type does not support client media")
	}
	if math.IsNaN(plan.DurationSeconds) || math.IsInf(plan.DurationSeconds, 0) || plan.DurationSeconds <= 0 || plan.DurationSeconds > 7*24*3600 {
		return fmt.Errorf("invalid media duration")
	}
	if len(plan.Artifacts) < 2 || len(plan.Artifacts) > maxClientMediaArtifacts {
		return fmt.Errorf("invalid artifact count")
	}
	seen := map[string]bool{}
	total := int64(0)
	playlistBytes := int64(0)
	playlistCount := 0
	required := map[string]bool{"master.m3u8": false}
	if plan.Kind == "audio" {
		required["spectrogram.png"] = false
		required["waveform.json"] = false
	} else {
		required["thumbnail.webp"] = false
	}
	for _, artifact := range plan.Artifacts {
		if !clientMediaPathPattern.MatchString(artifact.Path) || seen[artifact.Path] || artifact.Size <= 0 || artifact.Size > maxClientMediaArtifactSize {
			return fmt.Errorf("invalid artifact path or size")
		}
		seen[artifact.Path] = true
		digest, err := artifact.digest()
		if err != nil || len(digest) != 32 || strings.ToLower(artifact.SHA256) != artifact.SHA256 {
			return fmt.Errorf("invalid artifact SHA256")
		}
		mime, deriv, err := clientMediaArtifactPolicy(artifact.Path, plan.Kind)
		if err != nil || normalizeMimeType(artifact.MimeType) != mime || artifact.DerivativeType != int32(deriv) {
			return fmt.Errorf("invalid artifact policy for %s", artifact.Path)
		}
		if strings.HasSuffix(artifact.Path, ".m3u8") {
			playlistCount++
			playlistBytes += artifact.Size
			if artifact.Size > 1<<20 || playlistCount > 16 || playlistBytes > 8<<20 {
				return fmt.Errorf("playlist budget exceeded")
			}
		}
		if artifact.Path == "waveform.json" && artifact.Size > 128<<10 {
			return fmt.Errorf("waveform too large")
		}
		total += artifact.Size
		if total > maxClientMediaTotalSize {
			return fmt.Errorf("bundle too large")
		}
		if _, ok := required[artifact.Path]; ok {
			required[artifact.Path] = true
		}
	}
	for name, found := range required {
		if !found {
			return fmt.Errorf("missing required artifact %s", name)
		}
	}
	return nil
}
func clientMediaArtifactPolicy(name, kind string) (string, managev1.FileDerivativeType, error) {
	const unset = managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_UNSPECIFIED
	switch name {
	case "spectrogram.png":
		if kind == "audio" {
			return "image/png", clientSpectrogram, nil
		}
	case "waveform.json":
		if kind == "audio" {
			return "application/json", clientWaveform, nil
		}
	case "thumbnail.webp":
		if kind == "video" {
			return "image/webp", clientThumbnail, nil
		}
	default:
		switch filepath.Ext(name) {
		case ".m3u8":
			return "application/vnd.apple.mpegurl", clientHLS, nil
		case ".ts":
			return "video/mp2t", clientHLS, nil
		}
	}
	return "", unset, fmt.Errorf("unsupported artifact")
}
func decodeClientMediaPlan(session model.UploadSession) (clientMediaPlan, error) {
	var plan clientMediaPlan
	if session.ClientMediaManifest == nil || session.ClientMediaBundleID == nil {
		return plan, fmt.Errorf("client media plan missing")
	}
	if err := json.Unmarshal([]byte(*session.ClientMediaManifest), &plan); err != nil {
		return plan, err
	}
	return plan, validateClientMediaPlan(plan, session)
}
func normalizeClientMediaPlan(plan *clientMediaPlan) {
	sort.Slice(plan.Artifacts, func(i, j int) bool { return plan.Artifacts[i].Path < plan.Artifacts[j].Path })
}
func clientMediaStagingKey(bundleID, path string) string {
	return "upload-media/" + bundleID + "/" + path
}

// Direct browser media must arrive with completed derivatives. Remote imports
// use artifact separate ingest path and retain their server processing workflow.
func requireDirectClientMediaBundle(uploadType managev1.UploadType, verifiedMime string, session model.UploadSession, bundleID string) error {
	if !supportsClientMedia(uploadType) {
		return nil
	}
	if !strings.HasPrefix(verifiedMime, "audio/") && !strings.HasPrefix(verifiedMime, "video/") {
		return nil
	}
	if session.ClientMediaBundleID == nil || session.ClientMediaManifest == nil || bundleID == "" {
		return errs.FailedPrecondition("clientMediaBundleRequired: direct audio and video uploads require completed browser media")
	}
	if err := clientMediaBundleMatches(session, bundleID); err != nil {
		return err
	}
	plan, err := decodeClientMediaPlan(session)
	if err != nil {
		return errs.FailedPrecondition("invalid prepared client media manifest")
	}
	if !strings.HasPrefix(verifiedMime, plan.Kind+"/") {
		return errs.FailedPrecondition("client media kind differs from verified original")
	}
	return nil
}

func supportsClientMedia(uploadType managev1.UploadType) bool {
	switch uploadType {
	case managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE, managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO, managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO, managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO:
		return true
	default:
		return false
	}
}

func encodeClientMediaPlan(plan clientMediaPlan) (string, error) {
	normalizeClientMediaPlan(&plan)
	encoded, err := json.Marshal(plan)
	return string(encoded), err
}

func (plan clientMediaPlan) artifact(path string) *clientMediaArtifact {
	for i := range plan.Artifacts {
		if plan.Artifacts[i].Path == path {
			return &plan.Artifacts[i]
		}
	}
	return nil
}

func (artifact clientMediaArtifact) isHLS() bool {
	return managev1.FileDerivativeType(artifact.DerivativeType) == clientHLS
}

func (artifact clientMediaArtifact) assetKind() string {
	return strings.TrimSuffix(artifact.Path, filepath.Ext(artifact.Path))
}

func (artifact clientMediaArtifact) extension() string {
	return strings.TrimPrefix(filepath.Ext(artifact.Path), ".")
}

func (artifact clientMediaArtifact) digest() ([]byte, error) {
	return hex.DecodeString(artifact.SHA256)
}
