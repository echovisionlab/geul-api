package referencecatalogtest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/referencecatalog/referencecatalogtest"
)

func TestFakeStoreKeepsTheReferenceCatalogStoreContract(t *testing.T) {
	referencecatalogtest.RunStoreContract(t, func(*testing.T) referencecatalogtest.Harness { return referencecatalogtest.FakeHarness() })
}
