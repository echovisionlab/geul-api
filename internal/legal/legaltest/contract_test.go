package legaltest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/legal/legaltest"
)

func TestFakeStoreKeepsTheLegalStoreContract(t *testing.T) {
	legaltest.RunStoreContract(t, func(*testing.T) legaltest.Harness { return legaltest.FakeHarness() })
}
