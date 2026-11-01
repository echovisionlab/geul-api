package authenticationtest_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/authentication/authenticationtest"
)

func TestFakeStoreKeepsTheStoreContract(t *testing.T) {
	authenticationtest.RunStoreContract(t, func(*testing.T) authenticationtest.Harness { return authenticationtest.FakeHarness() })
}
