package legal

import (
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// TermsStatusToProto maps a stored Terms status to the API enum. A value the
// API does not know is UNSPECIFIED.
func TermsStatusToProto(status model.LegalPolicyStatus) managev1.TermsStatus {
	switch status {
	case model.TermsStatusDraft:
		return managev1.TermsStatus_TERMS_STATUS_DRAFT
	case model.TermsStatusScheduled:
		return managev1.TermsStatus_TERMS_STATUS_SCHEDULED
	case model.TermsStatusActive:
		return managev1.TermsStatus_TERMS_STATUS_ACTIVE
	case model.TermsStatusArchived:
		return managev1.TermsStatus_TERMS_STATUS_ARCHIVED
	default:
		return managev1.TermsStatus_TERMS_STATUS_UNSPECIFIED
	}
}

// TermsStatusFromProto maps an API Terms status to the stored value. It
// reports false for UNSPECIFIED and unknown values.
func TermsStatusFromProto(status managev1.TermsStatus) (model.LegalPolicyStatus, bool) {
	switch status {
	case managev1.TermsStatus_TERMS_STATUS_DRAFT:
		return model.TermsStatusDraft, true
	case managev1.TermsStatus_TERMS_STATUS_SCHEDULED:
		return model.TermsStatusScheduled, true
	case managev1.TermsStatus_TERMS_STATUS_ACTIVE:
		return model.TermsStatusActive, true
	case managev1.TermsStatus_TERMS_STATUS_ARCHIVED:
		return model.TermsStatusArchived, true
	default:
		return "", false
	}
}

// PrivacyStatusToProto maps a stored Privacy status to the API enum. A value
// the API does not know is UNSPECIFIED.
func PrivacyStatusToProto(status model.LegalPolicyStatus) managev1.PrivacyStatus {
	switch status {
	case model.PrivacyStatusDraft:
		return managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT
	case model.PrivacyStatusScheduled:
		return managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED
	case model.PrivacyStatusActive:
		return managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE
	case model.PrivacyStatusArchived:
		return managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED
	default:
		return managev1.PrivacyStatus_PRIVACY_STATUS_UNSPECIFIED
	}
}

// PrivacyStatusFromProto maps an API Privacy status to the stored value. It
// reports false for UNSPECIFIED and unknown values.
func PrivacyStatusFromProto(status managev1.PrivacyStatus) (model.LegalPolicyStatus, bool) {
	switch status {
	case managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT:
		return model.PrivacyStatusDraft, true
	case managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED:
		return model.PrivacyStatusScheduled, true
	case managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE:
		return model.PrivacyStatusActive, true
	case managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED:
		return model.PrivacyStatusArchived, true
	default:
		return "", false
	}
}
