package menutest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/menu/menutest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

func TestFakeStoreKeepsTheMenuStoreContract(t *testing.T) {
	menutest.RunStoreContract(t, func(*testing.T) mutationtest.Harness[menu.Store] {
		return mutationtest.FakeHarness[menu.Store](menutest.NewStore())
	})
}
