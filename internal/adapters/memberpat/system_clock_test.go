package memberpat

import (
	"testing"
	"time"
)

func TestSystemClockReadsAtPostgreSQLPrecision(t *testing.T) {
	t.Parallel()

	now := SystemClock{}.Now()
	if now.Location() != time.UTC || now.Nanosecond()%int(time.Microsecond) != 0 {
		t.Fatalf("SystemClock.Now() = %v, want UTC without sub-microsecond digits", now)
	}
}
