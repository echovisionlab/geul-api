package legal

import (
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// TermsStatusToProto maps a stored Terms status to the API enum. A value the
// API does not know is UNSPECIFIED.
func TermsStatusToProto(status Status) managev1.TermsStatus {
	switch status {
	case TermsDraft:
		return managev1.TermsStatus_TERMS_STATUS_DRAFT
	case TermsScheduled:
		return managev1.TermsStatus_TERMS_STATUS_SCHEDULED
	case TermsActive:
		return managev1.TermsStatus_TERMS_STATUS_ACTIVE
	case TermsArchived:
		return managev1.TermsStatus_TERMS_STATUS_ARCHIVED
	default:
		return managev1.TermsStatus_TERMS_STATUS_UNSPECIFIED
	}
}

// TermsStatusFromProto maps an API Terms status to the stored value. It
// reports false for UNSPECIFIED and unknown values.
func TermsStatusFromProto(status managev1.TermsStatus) (Status, bool) {
	switch status {
	case managev1.TermsStatus_TERMS_STATUS_DRAFT:
		return TermsDraft, true
	case managev1.TermsStatus_TERMS_STATUS_SCHEDULED:
		return TermsScheduled, true
	case managev1.TermsStatus_TERMS_STATUS_ACTIVE:
		return TermsActive, true
	case managev1.TermsStatus_TERMS_STATUS_ARCHIVED:
		return TermsArchived, true
	default:
		return "", false
	}
}

// PrivacyStatusToProto maps a stored Privacy status to the API enum. A value
// the API does not know is UNSPECIFIED.
func PrivacyStatusToProto(status Status) managev1.PrivacyStatus {
	switch status {
	case PrivacyDraft:
		return managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT
	case PrivacyScheduled:
		return managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED
	case PrivacyActive:
		return managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE
	case PrivacyArchived:
		return managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED
	default:
		return managev1.PrivacyStatus_PRIVACY_STATUS_UNSPECIFIED
	}
}

// PrivacyStatusFromProto maps an API Privacy status to the stored value. It
// reports false for UNSPECIFIED and unknown values.
func PrivacyStatusFromProto(status managev1.PrivacyStatus) (Status, bool) {
	switch status {
	case managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT:
		return PrivacyDraft, true
	case managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED:
		return PrivacyScheduled, true
	case managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE:
		return PrivacyActive, true
	case managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED:
		return PrivacyArchived, true
	default:
		return "", false
	}
}
