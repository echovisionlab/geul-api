package memberpat

import "time"

// SystemClock supplies wall-clock time at the server composition boundary.
// Domain services still receive it through the explicit pat.Clock contract.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }
