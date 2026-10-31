// Package dbtime produces timestamps at the precision PostgreSQL stores.
package dbtime

import "time"

// Precision is the resolution of PostgreSQL timestamp and timestamptz columns.
const Precision = time.Microsecond

// Now returns the current UTC time truncated to Precision. Linux clocks report
// nanoseconds that PostgreSQL drops on write, so a mutation that stores and
// returns time.Now() answers with a value no later read reproduces.
func Now() time.Time {
	return time.Now().UTC().Truncate(Precision)
}
