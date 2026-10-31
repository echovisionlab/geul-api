package emaildelivery

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Stored values equal the API enum names, so every API type except
// UNSPECIFIED must round-trip; a new API type fails here until it is mapped.
func TestMailAdapterTypeMappingCoversEveryAPIType(t *testing.T) {
	t.Parallel()
	for value, name := range managev1.MailAdapterType_name {
		adapterType := managev1.MailAdapterType(value)
		stored, known := mailAdapterTypeFromProto(adapterType)
		if adapterType == managev1.MailAdapterType_MAIL_ADAPTER_TYPE_UNSPECIFIED {
			if known {
				t.Fatalf("UNSPECIFIED mapped to %q", stored)
			}
			continue
		}
		if !known || string(stored) != name || mailAdapterTypeToProto(stored) != adapterType {
			t.Fatalf("%s maps to %q (known %t) and back to %s", name, stored, known, mailAdapterTypeToProto(stored))
		}
	}
	if knownMailAdapterType(model.MailAdapterType("MAIL_ADAPTER_TYPE_RETIRED")) {
		t.Fatal("unknown stored type is known")
	}
}
