package translation

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"time"
)

const targetRevisionPrefix = "tr1_"

// TargetRevisionFacts are existing owning-domain facts used to derive an
// opaque target write CAS. They are not translation freshness or history.
type TargetRevisionFacts struct {
	LocaleExists     bool
	DocumentRevision string
	LocaleUpdatedAt  *time.Time
}

// TargetRevisionConflict reports a failed missing-row or exact opaque token
// comparison. CurrentRevision is empty only when the locale row is absent.
type TargetRevisionConflict struct {
	CurrentRevision string
	CurrentExists   bool
}

func (e *TargetRevisionConflict) Error() string {
	if e.CurrentExists {
		return "translation target revision conflict"
	}
	return "translation target no longer exists"
}

// DeriveTargetRevision returns no token for an absent target. Present scalar
// targets use their locked locale-row timestamp; Block-backed and mixed
// targets additionally bind the current Content Document revision.
func DeriveTargetRevision(facts TargetRevisionFacts) (string, error) {
	if !facts.LocaleExists {
		if facts.DocumentRevision != "" || facts.LocaleUpdatedAt != nil {
			return "", fmt.Errorf("absent translation target cannot carry revision facts")
		}
		return "", nil
	}
	if facts.LocaleUpdatedAt == nil || facts.LocaleUpdatedAt.IsZero() {
		return "", fmt.Errorf("translation target updated_at is required")
	}

	hash := sha256.New()
	writeRevisionPart(hash.Write, facts.DocumentRevision)
	var timestamp [8]byte
	binary.BigEndian.PutUint64(timestamp[:], uint64(facts.LocaleUpdatedAt.UTC().UnixNano()))
	_, _ = hash.Write(timestamp[:])
	return targetRevisionPrefix + base64.RawURLEncoding.EncodeToString(hash.Sum(nil)), nil
}

// ValidateExpectedTargetRevision enforces absent-row create and present-row
// exact CAS after the owning adapter has acquired its authoritative locks and
// rederived currentRevision.
func ValidateExpectedTargetRevision(expected *string, currentRevision string, currentExists bool) error {
	return ValidateTargetRevisionWrite(expected, currentRevision, currentExists, false)
}

// ValidateTargetRevisionWrite is the single missing-vs-existing target CAS
// boundary. Provider delivery may opt into the exact target state observed
// under the owning domain lock; interactive callers must supply that token.
func ValidateTargetRevisionWrite(
	expected *string,
	currentRevision string,
	currentExists bool,
	useCurrent bool,
) error {
	if useCurrent {
		expected = nil
		if currentExists {
			current := currentRevision
			expected = &current
		}
	}
	if !currentExists {
		if currentRevision != "" {
			return fmt.Errorf("absent translation target has a revision")
		}
		if expected == nil {
			return nil
		}
		return &TargetRevisionConflict{}
	}
	if currentRevision == "" {
		return fmt.Errorf("present translation target has no revision")
	}
	if expected == nil || *expected != currentRevision {
		return &TargetRevisionConflict{CurrentRevision: currentRevision, CurrentExists: true}
	}
	return nil
}

// NextTargetUpdatedAt returns a PostgreSQL-compatible timestamp that strictly
// advances an existing locale row even when the application clock has not.
// Domain adapters call it while holding the same row/root lock used for CAS.
func NextTargetUpdatedAt(now time.Time, current time.Time) time.Time {
	next := now.UTC().Truncate(time.Microsecond)
	current = current.UTC().Truncate(time.Microsecond)
	if !next.After(current) {
		return current.Add(time.Microsecond)
	}
	return next
}

func writeRevisionPart(write func([]byte) (int, error), value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = write(length[:])
	_, _ = write([]byte(value))
}
