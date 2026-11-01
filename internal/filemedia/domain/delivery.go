package domain

import (
	"context"

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
type DeliveryStore interface {
	LockDeliveryPrincipal(context.Context, *auth.UserInfo) (bool, error)
	LockDeliveryFiles(context.Context, []string, DeliveryFileScope) (map[string]File, error)
	LockMeshDeliveryWitnesses(context.Context, []MeshDeliveryWitness) (map[string]bool, error)
	LockAvatarFileBinding(context.Context, string, string) (bool, error)
	LockUsageDeliveryFile(context.Context, string) (File, bool, error)
	LockDeliveryOwner(context.Context, string, string) (DeliveryOwner, bool, error)
	LockExactDeliveryUsage(context.Context, DeliveryUsageWitness) (bool, error)
}
