package aidocumentadapter

import (
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
)

func TestNewArtistRegistrationRequiresOwningService(t *testing.T) {
	if _, err := NewArtistRegistration(nil); err == nil {
		t.Fatal("NewArtistRegistration(nil) succeeded")
	}
}

func TestArtistIdentityFailsClosed(t *testing.T) {
	for _, identity := range []core.DocumentIdentity{
		{Domain: core.DomainRelease, Reference: "019c89aa-6798-7a37-8532-11e03f729c35"},
		{Domain: core.DomainArtist, Reference: "not-a-uuid"},
	} {
		if err := validateArtistDocumentIdentity(identity); err == nil {
			t.Fatalf("validateArtistDocumentIdentity(%+v) succeeded", identity)
		}
	}
}
