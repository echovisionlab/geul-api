package publictest

import "testing"

func TestFakePublicStoreContract(t *testing.T) {
	RunStoreContract(t, func(*testing.T) Harness { return FakeHarness(NewStore()) })
}
