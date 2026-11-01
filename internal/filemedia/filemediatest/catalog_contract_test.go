package filemediatest

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

func TestFakeCatalogStoreContract(t *testing.T) {
	RunCatalogContract(t, func(t *testing.T) CatalogHarness {
		s := NewLibraryStore()
		return CatalogHarness{SeedScoped: func(_ *testing.T, kind, id string, owner ScopedOwner, attachment domain.ScopedAttachment) {
			s.ScopedOwners[kind+":"+id] = owner
			s.ScopedAttachments = append(s.ScopedAttachments, ScopedAttachmentRecord{OwnerKind: kind, OwnerID: id, DocumentID: owner.DocumentID, Attachment: attachment})
		}, SeedPolicy: func(_ *testing.T, r domain.PolicyRelation, ids []string) {
			s.Policies[PolicyKey(r.Selector)] = PolicyRecord{Relation: r, SegmentIDs: ids}
		}, SeedSegment: func(_ *testing.T, segment domain.PolicySegment) { s.Segments[segment.ID] = segment }, Audit: s.AuditRecorder(), Harness: mutationtest.FakeHarness[domain.Store](s), SeedAssetBinding: func(t *testing.T, asset, id string) {
			require.NoError(t, s.MediaAssets.InsertBinding(t.Context(), mediaasset.AssetBinding{AssetID: asset, SourceFileID: &id, OwnerType: "post", OwnerID: "owner", BindingKey: "featured_image"}))
		}, Asset: func(t *testing.T, id string) mediaasset.Asset {
			a, err := s.MediaAssets.Asset(t.Context(), id)
			require.NoError(t, err)
			return a
		}, Outbox: func(*testing.T) []domain.DeletionTarget { return s.DeletionOutbox }, AuditCount: func(*testing.T) int { return len(s.Audits) }, Mesh: func(_ *testing.T, id string) (MeshRecord, bool) { m, ok := s.MeshRecords[id]; return m, ok }, SeedAsset: func(_ *testing.T, a mediaasset.Asset) { s.PutMediaAsset(a) }, SeedGeneration: func(_ *testing.T, g mediaasset.Generation) { s.PutMediaGeneration(g) }, SeedDerivative: func(_ *testing.T, r domain.StoredDerivativeDelivery) {
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
			s.References[fileID] = append(s.References[fileID], "post.featured_image_file_id")
			s.Usages = append(s.Usages, domain.FileUsageRow{FileID: fileID, Domain: kind, EntityID: id, ReferencePath: "featured_image", Title: &id})
			s.DeliveryOwners[kind+"\x00"+id] = domain.DeliveryOwner{ID: id, ContentDocumentID: &doc, Status: status}
			s.ExactUsages[domain.DeliveryUsageWitness{Kind: "featured_image", ResourceType: kind, ResourceID: id, FileID: fileID}.Key()] = true
		}, SeedFile: func(_ *testing.T, row domain.CatalogRow, pending, output bool) {
			s.Files[row.ID] = CatalogFileRecord{Row: row, PendingDeletion: pending, MeshOutput: output}
		}}
	})
}
