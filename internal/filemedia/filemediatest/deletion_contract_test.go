package filemediatest

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/application"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

func TestFakeDeletionStoreContract(t *testing.T) {
	RunDeletionContract(t, func(t *testing.T) DeletionHarness {
		s := NewDeletionStore()
		return DeletionHarness{Harness: mutationtest.FakeHarness[application.DeletionStore](s), SeedFile: func(_ *testing.T, f filemedia.File) { s.Files[f.ID] = f }, SeedGeneration: func(_ *testing.T, g mediaasset.Generation) {
			s.Generations[g.FileID] = append(s.Generations[g.FileID], g)
		}, SeedMeshResult: func(_ *testing.T, id, status string, selected bool) {
			if !selected && (status == "pending" || status == "processing" || status == "cancelled") {
				s.MeshResults[id]++
			}
		}, SeedReference: func(_ *testing.T, id string) { s.References[id] = []string{"client.logo_file_ids"} }}
	})
}
