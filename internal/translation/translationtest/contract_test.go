package translationtest

import "testing"

func TestFakeStoreKeepsTheTranslationStoreContract(t *testing.T) {
	RunStoreContract(t, func(*testing.T) Harness { return FakeHarness() })
}
