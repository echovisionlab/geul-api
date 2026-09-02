package llm

import "errors"

// ProviderFailureCategory is the bounded outcome of an LLM provider call.
// It intentionally excludes provider messages and payloads.
type ProviderFailureCategory string

const (
	ProviderFailureConfiguration   ProviderFailureCategory = "configuration"
	ProviderFailureAuthentication  ProviderFailureCategory = "authentication"
	ProviderFailureRateLimited     ProviderFailureCategory = "rate_limited"
	ProviderFailureUnavailable     ProviderFailureCategory = "unavailable"
	ProviderFailureRejected        ProviderFailureCategory = "rejected"
	ProviderFailureResponseInvalid ProviderFailureCategory = "response_invalid"
	ProviderFailureInternal        ProviderFailureCategory = "internal"
)

// ProviderExceptionType is a bounded diagnostic type. It must never contain a
// provider-supplied error string or Go type name.
type ProviderExceptionType string

const (
	ProviderExceptionUnknown          ProviderExceptionType = "unknown"
	ProviderExceptionGenAIAPI         ProviderExceptionType = "genai.api_error"
	ProviderExceptionDeepLHTTP        ProviderExceptionType = "deepl.http_error"
	ProviderExceptionDocumentNotFound ProviderExceptionType = "provider.document_not_found"
	ProviderExceptionContextDeadline  ProviderExceptionType = "context.deadline_exceeded"
	ProviderExceptionHTTPRequest      ProviderExceptionType = "http.request_error"
	ProviderExceptionResponseRead     ProviderExceptionType = "response.read_error"
	ProviderExceptionRequestEncode    ProviderExceptionType = "request.encode_error"
	ProviderExceptionRequestBuild     ProviderExceptionType = "request.build_error"
	ProviderExceptionResponseDecode   ProviderExceptionType = "response.decode_error"
	ProviderExceptionProviderResponse ProviderExceptionType = "provider.response_error"
	ProviderExceptionEmptyResponse    ProviderExceptionType = "provider.empty_response"
)

// ProviderFailureDetails is the redacted provider diagnosis that may be used
// by translation lifecycle classification and client spans.
type ProviderFailureDetails struct {
	Category        ProviderFailureCategory
	HTTPStatusClass string
	ExceptionType   ProviderExceptionType
}

// ProviderFailure preserves the underlying cause for local control flow while
// exposing only bounded diagnostics through Error and Details.
type ProviderFailure struct {
	details ProviderFailureDetails
	cause   error
}

func (e *ProviderFailure) Error() string {
	return "provider failure: " + string(e.details.Category)
}

func (e *ProviderFailure) Unwrap() error { return e.cause }

func (e *ProviderFailure) Details() ProviderFailureDetails { return e.details }

// NewProviderFailure converts an adapter error to its bounded contract. The
// returned error never renders provider response bodies or raw error strings.
func NewProviderFailure(details ProviderFailureDetails, cause error) error {
	return &ProviderFailure{details: normalizeProviderFailureDetails(details), cause: cause}
}

// ProviderFailureDetailsFromError returns the first typed provider failure in
// an error chain. Callers must not inspect the underlying provider error for
// telemetry or persisted failure state.
func ProviderFailureDetailsFromError(err error) (ProviderFailureDetails, bool) {
	var failure *ProviderFailure
	if !errors.As(err, &failure) || failure == nil {
		return ProviderFailureDetails{}, false
	}
	return failure.Details(), true
}

func normalizeProviderFailureDetails(details ProviderFailureDetails) ProviderFailureDetails {
	switch details.Category {
	case ProviderFailureConfiguration,
		ProviderFailureAuthentication,
		ProviderFailureRateLimited,
		ProviderFailureUnavailable,
		ProviderFailureRejected,
		ProviderFailureResponseInvalid,
		ProviderFailureInternal:
	default:
		details.Category = ProviderFailureInternal
	}

	switch details.HTTPStatusClass {
	case "4xx", "5xx":
	default:
		details.HTTPStatusClass = ""
	}

	switch details.ExceptionType {
	case ProviderExceptionGenAIAPI,
		ProviderExceptionDeepLHTTP,
		ProviderExceptionDocumentNotFound,
		ProviderExceptionContextDeadline,
		ProviderExceptionHTTPRequest,
		ProviderExceptionResponseRead,
		ProviderExceptionRequestEncode,
		ProviderExceptionRequestBuild,
		ProviderExceptionResponseDecode,
		ProviderExceptionProviderResponse,
		ProviderExceptionEmptyResponse,
		ProviderExceptionUnknown:
	default:
		details.ExceptionType = ProviderExceptionUnknown
	}
	return details
}

// NewProviderHTTPFailure classifies an HTTP response using only its status
// code. Response bodies and provider messages are deliberately not accepted.
func NewProviderHTTPFailure(statusCode int, exceptionType ProviderExceptionType, cause error) error {
	details := ProviderFailureDetails{ExceptionType: exceptionType}
	switch {
	case statusCode == 401 || statusCode == 403:
		details.Category = ProviderFailureAuthentication
		details.HTTPStatusClass = "4xx"
	case statusCode == 429:
		details.Category = ProviderFailureRateLimited
		details.HTTPStatusClass = "4xx"
	case statusCode >= 500 && statusCode <= 599:
		details.Category = ProviderFailureUnavailable
		details.HTTPStatusClass = "5xx"
	case statusCode >= 400 && statusCode <= 499:
		details.Category = ProviderFailureRejected
		details.HTTPStatusClass = "4xx"
	default:
		details.Category = ProviderFailureInternal
	}
	return NewProviderFailure(details, cause)
}
