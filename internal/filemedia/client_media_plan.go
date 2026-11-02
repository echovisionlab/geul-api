package filemedia

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
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
	clientHLS         = DerivativeHLS
	clientSpectrogram = DerivativeSpectrogram
	clientWaveform    = DerivativeWaveform
	clientThumbnail   = DerivativeThumbnail
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

func validateClientMediaPlan(plan clientMediaPlan, session domain.UploadSession) error {
	if err := validateClientMediaPlanHeader(plan, session); err != nil {
		return err
	}
	budget := newClientMediaBudget(plan.Kind)
	for _, artifact := range plan.Artifacts {
		if err := budget.admit(artifact, plan.Kind); err != nil {
			return err
		}
	}
	for name, found := range budget.required {
		if !found {
			return fmt.Errorf("missing required artifact %s", name)
		}
	}
	return nil
}

func validateClientMediaPlanHeader(plan clientMediaPlan, session domain.UploadSession) error {
	if !IsValidUUID(plan.MemberID) {
		return fmt.Errorf("invalid bundle member")
	}
	if plan.Version != clientMediaPlanVersion || (plan.Kind != "audio" && plan.Kind != "video") || !strings.HasPrefix(session.RequestedMime, plan.Kind+"/") {
		return fmt.Errorf("client media kind does not match original")
	}
	uploadType, _ := domain.ParseUploadType(session.UploadType)
	if !supportsClientMedia(uploadType) {
		return fmt.Errorf("upload type does not support client media")
	}
	if math.IsNaN(plan.DurationSeconds) || math.IsInf(plan.DurationSeconds, 0) || plan.DurationSeconds <= 0 || plan.DurationSeconds > 7*24*3600 {
		return fmt.Errorf("invalid media duration")
	}
	if len(plan.Artifacts) < 2 || len(plan.Artifacts) > maxClientMediaArtifacts {
		return fmt.Errorf("invalid artifact count")
	}
	return nil
}

// clientMediaBudget admits a plan's artifacts one at a time against the
// per-artifact, playlist and bundle limits, and records which of the kind's
// required artifacts it has seen.
type clientMediaBudget struct {
	seen          map[string]bool
	required      map[string]bool
	total         int64
	playlistBytes int64
	playlistCount int
}

func newClientMediaBudget(kind string) *clientMediaBudget {
	required := map[string]bool{"master.m3u8": false}
	if kind == "audio" {
		required["spectrogram.png"] = false
		required["waveform.json"] = false
	} else {
		required["thumbnail.webp"] = false
	}
	return &clientMediaBudget{seen: map[string]bool{}, required: required}
}

func (budget *clientMediaBudget) admit(artifact clientMediaArtifact, kind string) error {
	if !clientMediaPathPattern.MatchString(artifact.Path) || budget.seen[artifact.Path] || artifact.Size <= 0 || artifact.Size > maxClientMediaArtifactSize {
		return fmt.Errorf("invalid artifact path or size")
	}
	budget.seen[artifact.Path] = true
	digest, err := artifact.digest()
	if err != nil || len(digest) != 32 || strings.ToLower(artifact.SHA256) != artifact.SHA256 {
		return fmt.Errorf("invalid artifact SHA256")
	}
	mime, deriv, err := clientMediaArtifactPolicy(artifact.Path, kind)
	if err != nil || normalizeMimeType(artifact.MimeType) != mime || artifact.DerivativeType != int32(deriv) {
		return fmt.Errorf("invalid artifact policy for %s", artifact.Path)
	}
	if strings.HasSuffix(artifact.Path, ".m3u8") {
		budget.playlistCount++
		budget.playlistBytes += artifact.Size
		if artifact.Size > 1<<20 || budget.playlistCount > 16 || budget.playlistBytes > 8<<20 {
			return fmt.Errorf("playlist budget exceeded")
		}
	}
	if artifact.Path == "waveform.json" && artifact.Size > 128<<10 {
		return fmt.Errorf("waveform too large")
	}
	budget.total += artifact.Size
	if budget.total > maxClientMediaTotalSize {
		return fmt.Errorf("bundle too large")
	}
	if _, ok := budget.required[artifact.Path]; ok {
		budget.required[artifact.Path] = true
	}
	return nil
}
func clientMediaArtifactPolicy(name, kind string) (string, DerivativeKind, error) {
	const unset = DerivativeUnspecified
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
func decodeClientMediaPlan(session domain.UploadSession) (clientMediaPlan, error) {
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
func requireDirectClientMediaBundle(uploadType UploadType, verifiedMime string, session domain.UploadSession, bundleID string) error {
	if !supportsClientMedia(uploadType) {
		return nil
	}
	if !strings.HasPrefix(verifiedMime, "audio/") && !strings.HasPrefix(verifiedMime, "video/") {
		return nil
	}
	if session.ClientMediaBundleID == nil || session.ClientMediaManifest == nil || bundleID == "" {
		return domainerr.Precondition("clientMediaBundleRequired: direct audio and video uploads require completed browser media")
	}
	if err := clientMediaBundleMatches(session, bundleID); err != nil {
		return err
	}
	plan, err := decodeClientMediaPlan(session)
	if err != nil {
		return domainerr.Precondition("invalid prepared client media manifest").WithCause(failurecause.ServerFault(failurecause.WithAttrs(err, slog.String("upload_id", session.UploadID), slog.String("file_id", session.FileID))))
	}
	if !strings.HasPrefix(verifiedMime, plan.Kind+"/") {
		return domainerr.Precondition("client media kind differs from verified original")
	}
	return nil
}

func supportsClientMedia(uploadType UploadType) bool {
	switch uploadType {
	case UploadGeneralFile, UploadEditorAttachment,
		UploadEditorAudio, UploadEditorVideo, UploadTrackAudio:
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
	return DerivativeKind(artifact.DerivativeType) == clientHLS
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
