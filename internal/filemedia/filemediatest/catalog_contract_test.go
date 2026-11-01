package filemediatest

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

func TestFakeCatalogStoreContract(t *testing.T) {
	RunCatalogContract(t, func(*testing.T) CatalogHarness {
		s := NewLibraryStore()
		return CatalogHarness{Harness: mutationtest.FakeHarness[domain.Store](s), SeedAvatar: func(_ *testing.T, id, owner string) { s.AvatarOwners[id] = owner }, SeedMesh: func(_ *testing.T, w domain.MeshDeliveryWitness, status string) {
			if status == "ready" {
				s.ReadyMeshes[w.CandidateID] = w
			}
		}, SeedDeliveryOwner: func(_ *testing.T, kind, id, doc, status, fileID string) {
			s.DeliveryOwners[kind+"\x00"+id] = domain.DeliveryOwner{ID: id, ContentDocumentID: &doc, Status: status}
			s.ExactUsages[domain.DeliveryUsageWitness{Kind: "featured_image", ResourceType: kind, ResourceID: id, FileID: fileID}.Key()] = true
		}, SeedFile: func(_ *testing.T, row domain.CatalogRow, pending, output bool) {
			s.Files[row.ID] = CatalogFileRecord{Row: row, PendingDeletion: pending, MeshOutput: output}
		}}
	})
}
