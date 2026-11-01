package aitest

import "testing"

func TestFakeStoreKeepsTheMetadataAIStoreContract(t *testing.T) {
	RunStoreContract(t, func(*testing.T) Harness { return FakeHarness() })
}
