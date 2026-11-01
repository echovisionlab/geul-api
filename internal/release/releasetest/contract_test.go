package releasetest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/release/releasetest"
)

func TestFakeStoreKeepsTheReleaseStoreContract(t *testing.T) {
	releasetest.RunStoreContract(t, func(*testing.T) releasetest.Harness { return releasetest.FakeHarness() })
}
