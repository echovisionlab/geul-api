package emaildelivery

// AdapterType is the provider of a mail adapter. The values are the strings
// the mail_adapter_type column holds; connect_handler.go maps them to and
// from the API enum.
type AdapterType string

const (
	AdapterTypeLogging AdapterType = "MAIL_ADAPTER_TYPE_LOGGING"
	AdapterTypeSES     AdapterType = "MAIL_ADAPTER_TYPE_SES"
	AdapterTypeSMTP    AdapterType = "MAIL_ADAPTER_TYPE_SMTP"
)

// Known reports whether the API names t; a client may set only a known type.
func (t AdapterType) Known() bool {
	switch t {
	case AdapterTypeLogging, AdapterTypeSES, AdapterTypeSMTP:
		return true
	default:
		return false
	}
}
