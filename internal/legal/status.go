package legal

// Status is the stored lifecycle state of a version; its values are the
// strings the history tables hold, which name the kind.
type Status string

const (
	TermsDraft     Status = "TERMS_STATUS_DRAFT"
	TermsScheduled Status = "TERMS_STATUS_SCHEDULED"
	TermsActive    Status = "TERMS_STATUS_ACTIVE"
	TermsArchived  Status = "TERMS_STATUS_ARCHIVED"

	PrivacyDraft     Status = "PRIVACY_STATUS_DRAFT"
	PrivacyScheduled Status = "PRIVACY_STATUS_SCHEDULED"
	PrivacyActive    Status = "PRIVACY_STATUS_ACTIVE"
	PrivacyArchived  Status = "PRIVACY_STATUS_ARCHIVED"
)
