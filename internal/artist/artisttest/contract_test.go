package artisttest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/artist/artisttest"
)

func TestFakeStoreKeepsTheArtistStoreContract(t *testing.T) {
	artisttest.RunStoreContract(t, func(*testing.T) artisttest.Harness { return artisttest.FakeHarness() })
}
