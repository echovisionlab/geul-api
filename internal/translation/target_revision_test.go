package translation

import (
	"errors"
	"testing"
	"time"
)

func TestDeriveTargetRevisionUsesOnlyAuthoritativeCurrentFacts(t *testing.T) {
	updatedAt := time.Date(2026, time.August, 23, 4, 5, 6, 7000, time.UTC)
	base, err := DeriveTargetRevision(TargetRevisionFacts{LocaleExists: true, LocaleUpdatedAt: &updatedAt})
	if err != nil || base == "" {
		t.Fatalf("DeriveTargetRevision() = (%q, %v)", base, err)
	}
	stable, err := DeriveTargetRevision(TargetRevisionFacts{LocaleExists: true, LocaleUpdatedAt: &updatedAt})
	if err != nil || stable != base {
		t.Fatalf("stable revision = (%q, %v), want %q", stable, err, base)
	}

	nextTime := updatedAt.Add(time.Microsecond)
	for name, facts := range map[string]TargetRevisionFacts{
		"locale row changed": {LocaleExists: true, LocaleUpdatedAt: &nextTime},
		"document changed":   {LocaleExists: true, DocumentRevision: "document-revision-2", LocaleUpdatedAt: &updatedAt},
	} {
		t.Run(name, func(t *testing.T) {
			next, nextErr := DeriveTargetRevision(facts)
			if nextErr != nil || next == base {
				t.Fatalf("DeriveTargetRevision() = (%q, %v), must differ from %q", next, nextErr, base)
			}
		})
	}
}

func TestValidateExpectedTargetRevisionEnforcesMissingAndPresentCAS(t *testing.T) {
	current := "tr1_current"
	if err := ValidateExpectedTargetRevision(nil, "", false); err != nil {
		t.Fatalf("missing-row create rejected: %v", err)
	}
	if err := ValidateExpectedTargetRevision(&current, current, true); err != nil {
		t.Fatalf("matching present-row CAS rejected: %v", err)
	}

	for _, test := range []struct {
		name            string
		expected        *string
		currentRevision string
		exists          bool
	}{
		{name: "missing expected but row exists", currentRevision: current, exists: true},
		{name: "present expected but row missing", expected: &current},
		{name: "stale token", expected: stringPointerForTargetRevisionTest("tr1_old"), currentRevision: current, exists: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var conflict *TargetRevisionConflict
			if err := ValidateExpectedTargetRevision(test.expected, test.currentRevision, test.exists); !errors.As(err, &conflict) {
				t.Fatalf("ValidateExpectedTargetRevision() error = %v, want conflict", err)
			}
		})
	}
}

func TestValidateTargetRevisionWriteUsesLockedCurrentTarget(t *testing.T) {
	t.Parallel()

	if err := ValidateTargetRevisionWrite(nil, "current", true, true); err != nil {
		t.Fatalf("provider current target = %v", err)
	}
	if err := ValidateTargetRevisionWrite(nil, "", false, true); err != nil {
		t.Fatalf("provider missing target = %v", err)
	}
	if err := ValidateTargetRevisionWrite(nil, "", true, true); err == nil {
		t.Fatal("provider accepted a present target without a derived revision")
	}
}

func TestNextTargetUpdatedAtStrictlyAdvancesAtDatabasePrecision(t *testing.T) {
	current := time.Date(2026, time.August, 23, 1, 2, 3, 456789000, time.UTC)
	if got := NextTargetUpdatedAt(current.Add(-time.Second), current); !got.After(current) {
		t.Fatalf("NextTargetUpdatedAt() = %s, must advance %s", got, current)
	}
	want := current.Add(time.Second)
	if got := NextTargetUpdatedAt(want, current); got != want {
		t.Fatalf("NextTargetUpdatedAt() = %s, want %s", got, want)
	}
}

func stringPointerForTargetRevisionTest(value string) *string { return &value }

func TestDeriveTargetRevisionRepresentsMissingTargetAsAbsentToken(t *testing.T) {
	revision, err := DeriveTargetRevision(TargetRevisionFacts{})
	if err != nil || revision != "" {
		t.Fatalf("DeriveTargetRevision() = (%q, %v), want absent token", revision, err)
	}
	updatedAt := time.Now()
	if _, err := DeriveTargetRevision(TargetRevisionFacts{LocaleUpdatedAt: &updatedAt}); err == nil {
		t.Fatal("absent target accepted persisted revision facts")
	}
	if _, err := DeriveTargetRevision(TargetRevisionFacts{LocaleExists: true}); err == nil {
		t.Fatal("present target accepted no updated_at")
	}
}
