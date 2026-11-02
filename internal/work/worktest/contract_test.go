package worktest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/work/worktest"
)

func TestFakeStoreKeepsTheWorkStoreContract(t *testing.T) {
	worktest.RunStoreContract(t, func(*testing.T) worktest.Harness { return worktest.FakeHarness() })
}
