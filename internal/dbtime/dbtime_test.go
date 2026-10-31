package dbtime

import (
	"testing"
	"time"
)

func TestNowIsUTCAtPostgreSQLPrecision(t *testing.T) {
	now := Now()
	if now.Location() != time.UTC {
		t.Fatalf("Now() location = %v, want UTC", now.Location())
	}
	if now.Nanosecond()%int(Precision) != 0 {
		t.Fatalf("Now() = %v carries sub-microsecond digits", now)
	}
}
