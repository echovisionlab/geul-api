package ogtest

import "testing"

func TestFakeStoreKeepsTheStoreContract(t *testing.T) {
	t.Parallel()
	RunStoreContract(t, func(*testing.T) Harness { return FakeHarness() })
}
