package aidocument

import (
	"errors"
	"testing"
)

func TestDocumentInputFailuresAreTypedBeforeDomainAccess(t *testing.T) {
	for _, operation := range []string{"open", "describe", "read", "validate", "apply"} {
		t.Run(operation, func(t *testing.T) {
			port := &fakePort{document: testDocument("en", true)}
			service, _ := NewService(port)
			var err error
			switch operation {
			case "open":
				_, err = service.Open(t.Context(), OpenRequest{Document: port.document.Identity})
			case "describe":
				_, err = service.Describe(t.Context(), OpenRequest{Document: port.document.Identity})
			case "read":
				_, err = service.Read(t.Context(), ReadRequest{Document: port.document.Identity, Locale: "en", Mode: ReadFields})
			case "validate":
				_, err = service.Validate(t.Context(), ApplyRequest{})
			case "apply":
				_, err = service.Apply(t.Context(), ApplyRequest{})
			}
			var input *InputError
			if !errors.As(err, &input) || input.Message == "" || port.loadCalls != 0 || port.validateCalls != 0 || len(port.applied) != 0 {
				t.Fatalf("input failure=%v, port=%+v", err, port)
			}
		})
	}
}
