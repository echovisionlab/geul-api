package filemediatest

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

func TestFakeLibraryStoreContract(t *testing.T) {
	RunLibraryContract(t, func(*testing.T) mutationtest.Harness[domain.Store] {
		return mutationtest.FakeHarness[domain.Store](NewLibraryStore())
	})
}
