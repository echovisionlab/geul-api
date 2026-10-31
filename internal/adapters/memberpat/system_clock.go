package memberpat

import (
	"time"

	"github.com/echovisionlab/geul-api/internal/dbtime"
)

// SystemClock supplies wall-clock time at the server composition boundary.
// Domain services still receive it through the explicit pat.Clock contract.
// It reads at PostgreSQL precision so a stored PAT timestamp equals the value
// the service compares and returns.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return dbtime.Now() }
