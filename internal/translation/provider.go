package translation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type QualityTier string

const (
	QualityTierDraft    QualityTier = "draft"
	QualityTierStandard QualityTier = "standard"
	QualityTierHigh     QualityTier = "high"
)

type ContentKind string

const (
	ContentKindEditorial          ContentKind = "editorial"
	ContentKindDirectUserGuidance ContentKind = "direct_user_guidance"
	ContentKindLegal              ContentKind = "legal"
	ContentKindNavigation         ContentKind = "navigation"
)

type Register string

const (
	RegisterNeutralPlain   Register = "neutral_plain"
	RegisterPolite         Register = "polite"
	RegisterFormalDocument Register = "formal_document"
)

type RegisterPolicy string

const (
	RegisterPolicyTargetDefault           RegisterPolicy = "target_default"
	RegisterPolicyPreserveSourceWhenClear RegisterPolicy = "preserve_source_when_clear"
)

const (
	UnitIssueReasonEmptyTranslation = "empty_translation"
	UnitIssueReasonUnchangedSource  = "unchanged_from_source"
	UnitIssueReasonSourceResidue    = "source_residue"
	UnitIssueReasonMissingUnit      = "missing_unit"
)

// GenerationProfile defines non-score generation constraints for a locale translation run.
type GenerationProfile struct {
	QualityTier       QualityTier
	PreserveMarkup    bool
	ContentKind       ContentKind
	SourceLocale      string
	TargetLocale      string
	SourceRegister    Register
	TargetRegister    Register
	RegisterPolicy    RegisterPolicy
	MIMEType          string
	ProtectedTerms    []string
	StyleInstructions []string
}

// ProviderRequest is the generation-side provider contract.
type ProviderRequest struct {
	RequestID   string
	OperationID string
	Profile     GenerationProfile
	Document    XLIFFDocument
}

// UnitResult is a provider translation mapped back to the original unit id.
type UnitResult struct {
	UnitID         string
	TranslatedText string
	OriginalData   []XLIFFOriginalData
	TargetInline   []XLIFFInline
}

// ProviderResponse captures provider output before hard validation and apply.
type ProviderResponse struct {
	Document XLIFFDocument
	Raw      *ProviderRawResponse
}

// ProviderRawResponse retains the exact provider payload without exposing it
// through formatting, logs, RPCs, or telemetry. Callers receive defensive
// copies through Body.
type ProviderRawResponse struct {
	mediaType string
	body      []byte
}

func NewProviderRawResponse(mediaType string, body []byte) (*ProviderRawResponse, error) {
	mediaType = strings.TrimSpace(mediaType)
	if mediaType == "" {
		return nil, fmt.Errorf("provider response media type is required")
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("provider response body is required")
	}
	return &ProviderRawResponse{mediaType: mediaType, body: append([]byte(nil), body...)}, nil
}

func (r ProviderRawResponse) MediaType() string { return r.mediaType }

func (r ProviderRawResponse) Body() []byte { return append([]byte(nil), r.body...) }

func (ProviderRawResponse) String() string { return "[provider raw response]" }

func (ProviderRawResponse) GoString() string {
	return "translation.ProviderRawResponse{[redacted]}"
}

var (
	// ErrProviderDocumentNotFound means the provider explicitly reported that
	// an uploaded document no longer exists. Automatic resubmission is forbidden;
	// only an explicit administrator retry may clear the persisted handle.
	ErrProviderDocumentNotFound = errors.New("translation provider document not found")
	// ErrProviderProfileUnsupported means the selected provider cannot honor a
	// required generation profile. The job fails terminally without fallback.
	ErrProviderProfileUnsupported = errors.New("translation provider profile is unsupported")
	// ErrProviderResponseInvalid identifies structurally invalid or identity-
	// mismatched provider output without exposing provider response text.
	ErrProviderResponseInvalid = errors.New("translation provider response is invalid")
)

// ProviderDocumentHandle is the opaque provider identity required to resume a
// document translation. Its values are private and its formatting is redacted:
// the key must never be written to logs, RPC responses, or audit records.
type ProviderDocumentHandle struct {
	documentID  string
	documentKey string
}

func NewProviderDocumentHandle(documentID string, documentKey string) (ProviderDocumentHandle, error) {
	handle := ProviderDocumentHandle{
		documentID:  strings.TrimSpace(documentID),
		documentKey: strings.TrimSpace(documentKey),
	}
	if handle.documentID == "" || handle.documentKey == "" {
		return ProviderDocumentHandle{}, fmt.Errorf("provider document id and key are required")
	}
	return handle, nil
}

func (h ProviderDocumentHandle) DocumentID() string { return h.documentID }

func (h ProviderDocumentHandle) DocumentKey() string { return h.documentKey }

func (ProviderDocumentHandle) String() string { return "[provider document handle]" }

func (ProviderDocumentHandle) GoString() string {
	return "translation.ProviderDocumentHandle{[redacted]}"
}

type ProviderDocumentState string

const (
	ProviderDocumentPending  ProviderDocumentState = "pending"
	ProviderDocumentComplete ProviderDocumentState = "complete"
	ProviderDocumentError    ProviderDocumentState = "error"
	ProviderDocumentNotFound ProviderDocumentState = "not_found"
)

// ProviderDocumentCheck is the normalized result of one provider status
// request. PollAfter is always bounded when State is pending. ErrorMessage is
// diagnostic provider text and must not contain the opaque document handle.
type ProviderDocumentCheck struct {
	State        ProviderDocumentState
	PollAfter    time.Duration
	ErrorMessage string
}

// ResumableDocumentSession owns one provider adapter lifecycle while a caller
// persists the returned handle between upload, status checks, and download.
type ResumableDocumentSession interface {
	UploadDocument(ctx context.Context, req ProviderRequest) (ProviderDocumentHandle, error)
	CheckDocument(ctx context.Context, handle ProviderDocumentHandle) (ProviderDocumentCheck, error)
	DownloadDocument(
		ctx context.Context,
		req ProviderRequest,
		handle ProviderDocumentHandle,
	) (*ProviderResponse, error)
	Close(ctx context.Context) error
}

// ResumableDocumentGenerator is an optional capability. AI providers continue
// to implement the synchronous Generator contract only.
type ResumableDocumentGenerator interface {
	Generator
	StartDocumentSession(ctx context.Context, req ProviderRequest) (ResumableDocumentSession, error)
}

type GeneratorSession interface {
	Translate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error)
	Close(ctx context.Context) error
}

type Generator interface {
	StartSession(ctx context.Context, req ProviderRequest) (GeneratorSession, error)
	Translate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error)
	ProviderName() string
	ModelName() string
}

// HardValidationResult captures must-pass invariants for a translation candidate.
type HardValidationResult struct {
	Passed                     bool
	MissingUnitIDs             []string
	LineBreakMismatchUnitIDs   []string
	PlaceholderMismatchUnitIDs []string
	UnknownUnitIDs             []string
	DuplicateUnitIDs           []string
	MissingPlaceholders        []string
	UnexpectedPlaceholders     []string
	ParseError                 *string
}

func ValidateGenerationProfile(profile GenerationProfile) error {
	switch profile.QualityTier {
	case QualityTierDraft, QualityTierStandard, QualityTierHigh:
	default:
		return fmt.Errorf("unsupported translation quality tier %q", profile.QualityTier)
	}

	switch profile.ContentKind {
	case "", ContentKindEditorial, ContentKindDirectUserGuidance, ContentKindLegal, ContentKindNavigation:
	default:
		return fmt.Errorf("unsupported translation content kind %q", profile.ContentKind)
	}

	switch profile.SourceRegister {
	case "", RegisterNeutralPlain, RegisterPolite, RegisterFormalDocument:
	default:
		return fmt.Errorf("unsupported source register %q", profile.SourceRegister)
	}

	switch profile.TargetRegister {
	case "", RegisterNeutralPlain, RegisterPolite, RegisterFormalDocument:
	default:
		return fmt.Errorf("unsupported target register %q", profile.TargetRegister)
	}

	switch profile.RegisterPolicy {
	case "", RegisterPolicyTargetDefault, RegisterPolicyPreserveSourceWhenClear:
	default:
		return fmt.Errorf("unsupported register policy %q", profile.RegisterPolicy)
	}

	if strings.TrimSpace(profile.SourceLocale) == "" {
		return fmt.Errorf("source locale is required")
	}
	if strings.TrimSpace(profile.TargetLocale) == "" {
		return fmt.Errorf("target locale is required")
	}
	if strings.EqualFold(strings.TrimSpace(profile.SourceLocale), strings.TrimSpace(profile.TargetLocale)) {
		return fmt.Errorf("source and target locale must differ")
	}
	if strings.TrimSpace(profile.MIMEType) == "" {
		return fmt.Errorf("mime type is required")
	}
	return nil
}

func ValidateProviderRequest(req ProviderRequest) error {
	if strings.TrimSpace(req.RequestID) == "" {
		return fmt.Errorf("request id is required")
	}
	if strings.TrimSpace(req.OperationID) == "" {
		return fmt.Errorf("operation id is required")
	}
	if err := ValidateGenerationProfile(req.Profile); err != nil {
		return err
	}
	if err := ValidateXLIFFDocument(&req.Document, false); err != nil {
		return err
	}
	if req.Document.SourceLocale != req.Profile.SourceLocale || req.Document.TargetLocale != req.Profile.TargetLocale {
		return fmt.Errorf("XLIFF locales must match the translation generation profile")
	}

	return nil
}
