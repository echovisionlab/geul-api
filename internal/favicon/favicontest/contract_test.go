package favicontest

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

func TestFakeStoreContract(t *testing.T) {
	RunContract(t, func(t *testing.T) Harness {
		s, _ := New()
		return Harness{Harness: mutationtest.FakeHarness(favicon.Store(s)), SeedFile: func(_ *testing.T, id, mime string) { s.SeedFile(id, mime) }, SeedAsset: func(_ *testing.T, file, typ string, a favicon.DerivativeAsset) { s.SeedAsset(file, typ, a) }, SeedBinding: func(_ *testing.T, id string) { s.SeedBinding(id) }}
	})
}
