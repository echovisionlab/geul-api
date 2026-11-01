package campaigntest

import "testing"

func TestFakeStoreKeepsTheCampaignStoreContract(t *testing.T) {
	t.Parallel()
	RunStoreContract(t, FakeHarness)
}
