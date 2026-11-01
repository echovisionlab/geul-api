package emailauthoringtest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/emailauthoring/emailauthoringtest"
)

func TestFakeStoreKeepsTheEmailAuthoringStoreContract(t *testing.T) {
	t.Parallel()
	emailauthoringtest.RunStoreContract(t, emailauthoringtest.FakeHarness)
}
