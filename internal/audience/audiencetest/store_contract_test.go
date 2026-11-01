package audiencetest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/audience/audiencetest"
)

func TestFakeStoreKeepsTheStoreContract(t *testing.T) {
	audiencetest.RunStoreContract(t, func(*testing.T) audiencetest.Harness { return audiencetest.FakeHarness() })
}
