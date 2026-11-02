package mediaasset

import "time"

var ValidateAllocation = validatePublicAssetAllocation
var NormalizeOptionalString = normalizedOptionalString
var CanonicalMimeType = canonicalMimeType

const RetiredMediaGenerationRetention = retiredMediaGenerationRetention

func SetLifecycleClock(lifecycle *Lifecycle, now func() time.Time) { lifecycle.now = now }
