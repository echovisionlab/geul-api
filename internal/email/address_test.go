package email

import "testing"

func TestNormalizeAddressForDelivery(t *testing.T) {
	if got := NormalizeAddressForDelivery("  FAN@Example.COM "); got != "fan@example.com" {
		t.Fatalf("NormalizeAddressForDelivery() = %q", got)
	}
}
