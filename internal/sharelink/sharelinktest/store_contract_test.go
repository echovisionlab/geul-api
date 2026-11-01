package sharelinktest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/sharelink/sharelinktest"
)

func TestFakeStoreKeepsTheStoreContract(t *testing.T) {
	sharelinktest.RunStoreContract(t, func(*testing.T) sharelinktest.Harness { return sharelinktest.FakeHarness() })
}
