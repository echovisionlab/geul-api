package authentication

import (
	"fmt"
	"github.com/echovisionlab/geul-api/internal/httpadmission"
	"net/http"
)

const authCodeIPPolicy = "auth-code-ip"

// Only the caller IP budget is disclosed. Subject/global counts could reveal
// another account's activity or aggregate service usage.
func (q authCodeQuota) write(header http.Header) {
	if q.limit <= 0 {
		return
	}
	header.Set("Cache-Control", "no-store")
	header.Set(httpadmission.RateLimitPolicy, fmt.Sprintf(`"%s";q=%d;w=%d`, authCodeIPPolicy, q.limit, q.windowSeconds))
	header.Set(httpadmission.RateLimit, fmt.Sprintf(`"%s";r=%d;t=%d`, authCodeIPPolicy, q.remaining, q.windowSeconds))
}
