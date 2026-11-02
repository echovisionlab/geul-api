package domain

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"github.com/echovisionlab/geul-api/internal/auth"
)

type DeliveryFileScope int

const (
	CatalogDelivery DeliveryFileScope = iota
	StrongDelivery
)

type MeshDeliveryWitness struct{ CandidateID, SourceFileID, OutputFileID string }
type DeliveryUsageWitness struct{ Kind, FileID, ResourceType, ResourceID, RelationID, ReferencePath string }

func (w DeliveryUsageWitness) Key() string {
	return w.Kind + "\x00" + w.ResourceType + "\x00" + w.ResourceID + "\x00" + w.RelationID + "\x00" + w.ReferencePath + "\x00" + w.FileID
}

type DeliveryOwner struct {
	ID                string
	ContentDocumentID *string
	Status            string
}
type IngestBinding struct {
	FileID     string
	UploadType string
	EntityType *string
	EntityID   string
	CreatedAt  time.Time
}
type CurrentDeliveryUsage struct {
	FileID, ResourceType, ResourceID, Kind, RelationID, ReferencePath, OwnerFingerprint string
}
type MeshDeliveryOutput struct {
	ID, Status   string
	OutputFileID *string
}
type ProcessingFailureCounts struct{ Audio, Video, Waveform map[string]int64 }
type StoredFileDelivery struct {
	ClientMediaBundleID *string
	ID                  string
	Extension           string
	MimeType            string
	FileSize            int64
	DurationSeconds     *int
	FileName            *string
	IngestSlotID        *string
	IngestAttemptID     *string
}

type StoredDerivativeDelivery struct {
	FileID                  string
	Type                    string
	AssetID                 *string
	AssetKind               *string
	AssetExtension          *string
	AssetMimeType           *string
	AssetFileSize           *int64
	AssetSHA256             []byte
	AssetDisposition        *string
	AssetDownloadFilename   *string
	AssetStatus             *string
	MediaGenerationID       *string
	MediaGenerationFileID   *string
	MediaGenerationManifest *string
	MediaGenerationStatus   *string
}

type DeliveryStore interface {
	StoredFileDeliveries(context.Context, []string, bool) ([]StoredFileDelivery, error)
	StoredDerivativeDeliveries(context.Context, []string) ([]StoredDerivativeDelivery, error)
	MeshDeliveryOutputs(context.Context, string) ([]MeshDeliveryOutput, error)
	SourceAssetReferences(context.Context, string, string, []string) (map[string]*mediaasset.Reference, error)
	DeliveryProcessingFailures(context.Context, []string, []string) (ProcessingFailureCounts, error)
	DeliveryFileOwnership(context.Context, []string) ([]File, error)
	DeliveryIngestBindings(context.Context, []string) ([]IngestBinding, error)
	CurrentDeliveryUsages(context.Context, []string) ([]CurrentDeliveryUsage, error)
	LockDeliveryPrincipal(context.Context, *auth.UserInfo) (bool, error)
	LockDeliveryFiles(context.Context, []string, DeliveryFileScope) (map[string]File, error)
	LockMeshDeliveryWitnesses(context.Context, []MeshDeliveryWitness) (map[string]bool, error)
	LockAvatarFileBinding(context.Context, string, string) (bool, error)
	LockUsageDeliveryFile(context.Context, string) (File, bool, error)
	LockDeliveryOwner(context.Context, string, string) (DeliveryOwner, bool, error)
	LockExactDeliveryUsage(context.Context, DeliveryUsageWitness) (bool, error)
}
