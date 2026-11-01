package pagetest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/page/pagetest"
)

func TestFakeStoreKeepsThePageStoreContract(t *testing.T) {
	pagetest.RunStoreContract(t, func(*testing.T) pagetest.Harness { return pagetest.FakeHarness() })
}
