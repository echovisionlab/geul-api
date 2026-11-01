package programeventtest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/programevent/programeventtest"
)

func TestFakeStoreKeepsTheProgramEventStoreContract(t *testing.T) {
	programeventtest.RunStoreContract(t, func(*testing.T) programeventtest.Harness {
		return programeventtest.FakeHarness()
	})
}
