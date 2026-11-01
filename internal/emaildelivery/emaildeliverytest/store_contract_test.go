package emaildeliverytest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/emaildelivery/emaildeliverytest"
)

func TestFakeStoreKeepsTheStoreContract(t *testing.T) {
	emaildeliverytest.RunStoreContract(t, func(*testing.T) emaildeliverytest.Harness { return emaildeliverytest.FakeHarness() })
}
