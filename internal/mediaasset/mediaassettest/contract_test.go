package mediaassettest

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
)

func TestFakeStoreContract(t *testing.T) {
	RunStoreContract(t, func(t *testing.T) Harness {
		store, _ := New()
		return Harness{Harness: mutationtest.FakeHarness[mediaasset.Store](store),
			Cleanup: mutationtest.FakeHarness[mediaasset.CleanupStore](store), SeedFile: func(_ *testing.T, pending *time.Time) string {
				id := uuid.NewString()
				store.PutFile(id, pending)
				return id
			}, SeedGeneration: func(_ *testing.T, generation mediaasset.Generation) { store.PutGeneration(generation) }}
	})
}
