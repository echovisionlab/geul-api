package mcp

import (
	"strings"
	"testing"
)

func TestSharedUUIDParsingUsesCanonicalOwnerAcrossToolAndGatewayBoundaries(t *testing.T) {
	canonical := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if err := validateUUID("tool UUID", canonical); err != nil {
		t.Fatalf("tool UUID rejected canonical value: %v", err)
	}
	if !canonicalUUID(canonical) {
		t.Fatal("gateway rejected a canonical UUID")
	}
	for _, invalid := range []string{
		strings.ToUpper(canonical),
		"00000000-0000-0000-0000-000000000000",
		"aaaaaaaa-aaaa-0aaa-8aaa-aaaaaaaaaaaa",
		"{aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa}",
	} {
		if err := validateUUID("tool UUID", invalid); err == nil {
			t.Fatalf("tool accepted noncanonical UUID %q", invalid)
		}
		if canonicalUUID(invalid) {
			t.Fatalf("gateway accepted noncanonical UUID %q", invalid)
		}
	}
}
