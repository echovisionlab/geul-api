package emaildelivery

import (
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// mailAdapterTypeToProto maps a stored mail adapter type to the API enum. A
// value the API does not know is UNSPECIFIED.
func mailAdapterTypeToProto(adapterType model.MailAdapterType) managev1.MailAdapterType {
	switch adapterType {
	case model.MailAdapterTypeLogging:
		return managev1.MailAdapterType_MAIL_ADAPTER_TYPE_LOGGING
	case model.MailAdapterTypeSES:
		return managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES
	case model.MailAdapterTypeSMTP:
		return managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP
	default:
		return managev1.MailAdapterType_MAIL_ADAPTER_TYPE_UNSPECIFIED
	}
}

// mailAdapterTypeFromProto maps an API mail adapter type to the stored value.
// It reports false for UNSPECIFIED and unknown values, which a client may
// not set.
func mailAdapterTypeFromProto(adapterType managev1.MailAdapterType) (model.MailAdapterType, bool) {
	switch adapterType {
	case managev1.MailAdapterType_MAIL_ADAPTER_TYPE_LOGGING:
		return model.MailAdapterTypeLogging, true
	case managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES:
		return model.MailAdapterTypeSES, true
	case managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP:
		return model.MailAdapterTypeSMTP, true
	default:
		return "", false
	}
}

func knownMailAdapterType(adapterType model.MailAdapterType) bool {
	return mailAdapterTypeToProto(adapterType) != managev1.MailAdapterType_MAIL_ADAPTER_TYPE_UNSPECIFIED
}
