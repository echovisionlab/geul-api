package filemediatest

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

func TestFakeCatalogStoreContract(t *testing.T) {
	RunCatalogContract(t, func(*testing.T) CatalogHarness {
		s := NewLibraryStore()
		return CatalogHarness{Harness: mutationtest.FakeHarness[domain.Store](s), SeedAsset: func(_ *testing.T, a mediaasset.Asset) { s.MediaAssets.PutAsset(a) }, SeedGeneration: func(_ *testing.T, g mediaasset.Generation) { s.MediaAssets.PutGeneration(g) }, SeedDerivative: func(_ *testing.T, r domain.StoredDerivativeDelivery) {
			s.Derivatives[r.FileID] = append(s.Derivatives[r.FileID], r)
		}, SeedAvatar: func(_ *testing.T, id, owner string) {
			s.AvatarOwners[id] = owner
			s.IngestBindings[id] = domain.IngestBinding{FileID: id, UploadType: "UPLOAD_TYPE_USER_AVATAR", EntityID: owner}
		}, SeedMesh: func(_ *testing.T, w domain.MeshDeliveryWitness, status string) {
			s.MeshRecords[w.CandidateID] = MeshRecord{Witness: w, Status: status}
			if status == "ready" {
				s.ReadyMeshes[w.CandidateID] = w
			}
		}, SeedDeliveryOwner: func(_ *testing.T, kind, id, doc, status, fileID string) {
			s.DeliveryUsages = append(s.DeliveryUsages, domain.CurrentDeliveryUsage{Kind: "featured_image", FileID: fileID, ResourceType: kind, ResourceID: id, RelationID: id, ReferencePath: "featured_image", OwnerFingerprint: doc + ":" + status})
			s.DeliveryOwners[kind+"\x00"+id] = domain.DeliveryOwner{ID: id, ContentDocumentID: &doc, Status: status}
			s.ExactUsages[domain.DeliveryUsageWitness{Kind: "featured_image", ResourceType: kind, ResourceID: id, FileID: fileID}.Key()] = true
		}, SeedFile: func(_ *testing.T, row domain.CatalogRow, pending, output bool) {
			s.Files[row.ID] = CatalogFileRecord{Row: row, PendingDeletion: pending, MeshOutput: output}
		}}
	})
}
