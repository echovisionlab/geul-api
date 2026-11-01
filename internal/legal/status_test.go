package legal

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Stored values equal the API enum names, so every API status except
// UNSPECIFIED must round-trip; a new API status fails here until it is mapped.
func TestLegalPolicyStatusMappingCoversEveryAPIStatus(t *testing.T) {
	t.Parallel()
	for value, name := range managev1.TermsStatus_name {
		status := managev1.TermsStatus(value)
		stored, known := TermsStatusFromProto(status)
		if status == managev1.TermsStatus_TERMS_STATUS_UNSPECIFIED {
			if known {
				t.Fatalf("UNSPECIFIED mapped to %q", stored)
			}
			continue
		}
		if !known || string(stored) != name || TermsStatusToProto(stored) != status {
			t.Fatalf("%s maps to %q (known %t) and back to %s", name, stored, known, TermsStatusToProto(stored))
		}
	}
	for value, name := range managev1.PrivacyStatus_name {
		status := managev1.PrivacyStatus(value)
		stored, known := PrivacyStatusFromProto(status)
		if status == managev1.PrivacyStatus_PRIVACY_STATUS_UNSPECIFIED {
			if known {
				t.Fatalf("UNSPECIFIED mapped to %q", stored)
			}
			continue
		}
		if !known || string(stored) != name || PrivacyStatusToProto(stored) != status {
			t.Fatalf("%s maps to %q (known %t) and back to %s", name, stored, known, PrivacyStatusToProto(stored))
		}
	}
	if got := TermsStatusToProto(PrivacyDraft); got != managev1.TermsStatus_TERMS_STATUS_UNSPECIFIED {
		t.Fatalf("a Privacy status mapped to Terms %s", got)
	}
}
