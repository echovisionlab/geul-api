package filemediatest

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

func TestFakeCatalogStoreContract(t *testing.T) {
	RunCatalogContract(t, func(*testing.T) CatalogHarness {
		s := NewLibraryStore()
		return CatalogHarness{Harness: mutationtest.FakeHarness[domain.Store](s), SeedFile: func(_ *testing.T, row domain.CatalogRow, pending, output bool) {
			s.Files[row.ID] = CatalogFileRecord{Row: row, PendingDeletion: pending, MeshOutput: output}
		}}
	})
}
