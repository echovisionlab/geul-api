package emailtest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/email/emailtest"
)

func TestFakeStoreKeepsTheStoreContract(t *testing.T) {
	t.Parallel()
	emailtest.RunStoreContract(t, func(*testing.T) emailtest.Harness { return emailtest.FakeHarness() })
}
