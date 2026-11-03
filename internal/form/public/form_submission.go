package public

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/geoip"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const (
	formSubmissionSystemMetadataKeyPrefix = "__meta."

	formSubmissionMetaLocaleKey      = "__meta.locale"
	formSubmissionMetaCountryCodeKey = "__meta.countryCode"
	formSubmissionMetaTimeZoneKey    = "__meta.timeZone"
)

var formSubmissionMetaFieldLabels = map[string]string{
	formSubmissionMetaLocaleKey:      "Locale",
	formSubmissionMetaCountryCodeKey: "Country code",
	formSubmissionMetaTimeZoneKey:    "Time zone",
}

type formSubmissionSystemMetadata struct {
	ipAddress   *string
	countryCode *string
	userAgent   *string
	locale      *string
	timeZone    *string
}

// SubmitFormCommand submits a visitor's answers to a Form. ClientAddress,
// UserAgent and AcceptLanguage describe the request the answers came in.
type SubmitFormCommand struct {
	FormID         string
	Data           []byte
	Password       *string
	ClientAddress  string
	UserAgent      string
	AcceptLanguage string
}

// SubmitForm validates access and stores one form submission atomically. It
// returns the submission's ID.
func (s *Service) SubmitForm(ctx context.Context, command SubmitFormCommand) (string, error) {
	if err := validateFormSubmissionDataPayload(command.Data); err != nil {
		return "", err
	}
	memberID := authenticatedFormMemberID(ctx)
	metadata, err := s.resolveFormSubmissionMetadata(ctx, command, memberID)
	if err != nil {
		return "", err
	}
	var locked form.Form
	submission, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[form.Submission, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			locked, err = lockFormForSubmission(ctx, store, command.FormID)
			return err
		},
		// A visitor needs no session: the Form's own access policy, read
		// from the locked row, admits the submission.
		Access: mutation.Checked(func(ctx context.Context, store Store) error {
			return s.enforceLockedFormSubmissionAccess(ctx, store, &locked, command.Password, command.ClientAddress)
		}),
		Change: func(ctx context.Context, store Store) (form.Submission, error) {
			return createFormSubmission(ctx, store, &locked, command.Data, memberID, metadata)
		},
		Audit: func(submission form.Submission) mutation.Audit {
			return formSubmissionCreatedAudit(submission, memberID)
		},
	})
	if err != nil {
		return "", normalizeFormSubmissionError(err)
	}
	return submission.ID, nil
}

// formSubmissionCreatedAudit attributes the submission to its Member, or to
// an anonymous visitor.
func formSubmissionCreatedAudit(submission form.Submission, memberID *string) mutation.Audit {
	build := func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewFormSubmissionCreatedAuditRecord(metadata, submission.ID, submission.FormID)
	}
	if memberID != nil {
		return mutation.MemberAudit(*memberID, sharedtelemetry.AuditFormSubmissionCreated, build)
	}
	return mutation.AnonymousAudit(sharedtelemetry.AuditFormSubmissionCreated, build)
}

func authenticatedFormMemberID(ctx context.Context) *string {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.MemberID == "" {
		return nil
	}
	memberID := user.MemberID.String()
	return &memberID
}

func (s *Service) resolveFormSubmissionMetadata(
	ctx context.Context,
	command SubmitFormCommand,
	memberID *string,
) (formSubmissionSystemMetadata, error) {
	metadata := buildFormSubmissionSystemMetadata(ctx, command)
	if metadata.locale != nil || memberID == nil {
		return metadata, nil
	}
	locale, err := s.loadFormMemberPreferredLocale(ctx, *memberID)
	if err != nil {
		return formSubmissionSystemMetadata{}, err
	}
	metadata.locale = locale
	return metadata, nil
}

func (s *Service) loadFormMemberPreferredLocale(ctx context.Context, memberID string) (*string, error) {
	var preferred *string
	if err := s.read(ctx, func(store Store) error {
		var err error
		preferred, err = store.PreferredLocale(ctx, memberID)
		return err
	}); err != nil || preferred == nil {
		return nil, err
	}
	return localization.NormalizeSupportedLocale(*preferred), nil
}

func createFormSubmission(
	ctx context.Context,
	store Store,
	locked *form.Form,
	data []byte,
	memberID *string,
	metadata formSubmissionSystemMetadata,
) (form.Submission, error) {
	enriched, err := validateAndEnrichFormSubmission(ctx, store, locked.ID, data, metadata)
	if err != nil {
		return form.Submission{}, err
	}
	if err := enforceFormSubmissionLimits(ctx, store, locked, memberID); err != nil {
		return form.Submission{}, err
	}
	submission := form.Submission{
		FormID:      locked.ID,
		MemberID:    memberID,
		Data:        enriched,
		IPAddress:   metadata.ipAddress,
		CountryCode: metadata.countryCode,
		UserAgent:   metadata.userAgent,
		CreatedAt:   time.Now(),
	}
	if err := store.InsertSubmission(ctx, &submission); err != nil {
		return form.Submission{}, err
	}
	return submission, nil
}

func lockFormForSubmission(ctx context.Context, store Store, formID string) (form.Form, error) {
	locked, err := store.LockForm(ctx, formID)
	if errors.Is(err, form.ErrFormNotFound) {
		return locked, errFormNotFound()
	}
	return locked, err
}

func (s *Service) enforceLockedFormSubmissionAccess(
	ctx context.Context,
	store Store,
	locked *form.Form,
	password *string,
	clientAddress string,
) error {
	return s.enforceFormAccess(ctx, store, locked, formAccessOptions{
		context:                  ContextEmbed,
		hasValidPreviewToken:     false,
		draftAsNotFound:          false,
		enforcePassword:          true,
		password:                 password,
		clientAddress:            clientAddress,
		checkSubmissionLimit:     false,
		checkDuplicateSubmission: false,
	})
}

func validateAndEnrichFormSubmission(
	ctx context.Context,
	store Store,
	formID string,
	data []byte,
	metadata formSubmissionSystemMetadata,
) ([]byte, error) {
	sourceDocument, err := form.LoadCurrentFormSourceDocument(ctx, store, formID)
	if err != nil {
		return nil, err
	}
	if err := form.ValidateFormSubmissionAgainstSchema(ctx, sourceDocument.Schema, data); err != nil {
		return nil, domainerr.InvalidField("data", err.Error())
	}
	return mergeValidatedFormSubmissionSystemMetadata(data, metadata), nil
}

func enforceFormSubmissionLimits(ctx context.Context, store Store, locked *form.Form, memberID *string) error {
	if err := enforceFormSubmissionCapacity(ctx, store, locked); err != nil {
		return err
	}
	return enforceDuplicateFormSubmissionPolicy(ctx, store, locked, memberID)
}

func enforceFormSubmissionCapacity(ctx context.Context, store Store, locked *form.Form) error {
	if locked.MaxSubmissions == nil {
		return nil
	}
	count, err := store.CountSubmissions(ctx, locked.ID)
	if err != nil {
		return err
	}
	if count >= int64(*locked.MaxSubmissions) {
		return domainerr.Precondition("submission limit reached")
	}
	return nil
}

func enforceDuplicateFormSubmissionPolicy(ctx context.Context, store Store, locked *form.Form, memberID *string) error {
	if locked.AllowDuplicateSubmission == nil || *locked.AllowDuplicateSubmission || memberID == nil {
		return nil
	}
	exists, err := store.HasMemberSubmission(ctx, locked.ID, *memberID)
	if err != nil {
		return err
	}
	if exists {
		return domainerr.Precondition(msgFormAlreadySubmitted)
	}
	return nil
}

func normalizeFormSubmissionError(err error) error {
	if !mutation.IsClassified(err) && errors.Is(err, form.ErrFormNotFound) {
		return errFormNotFound()
	}
	return err
}

func buildFormSubmissionSystemMetadata(ctx context.Context, command SubmitFormCommand) formSubmissionSystemMetadata {
	metadata := formSubmissionSystemMetadata{}
	if command.ClientAddress != "" {
		ip := command.ClientAddress
		metadata.ipAddress = &ip
	}
	if userAgent := strings.TrimSpace(command.UserAgent); userAgent != "" {
		metadata.userAgent = &userAgent
	}
	metadata.locale = localization.InferPreferredLocaleFromAcceptLanguage(command.AcceptLanguage)
	if info := geoip.GetInfo(ctx); info != nil {
		metadata.countryCode = optionalTrimmedFormMetadata(info.CountryCode)
		metadata.timeZone = optionalTrimmedFormMetadata(info.TimeZone)
	}
	return metadata
}

func optionalTrimmedFormMetadata(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func mergeValidatedFormSubmissionSystemMetadata(
	rawData []byte,
	metadata formSubmissionSystemMetadata,
) []byte {
	var data structured.Fields
	_ = json.Unmarshal(rawData, &data)
	addFormSubmissionSystemMetadataValue(data, formSubmissionMetaLocaleKey, metadata.locale)
	addFormSubmissionSystemMetadataValue(data, formSubmissionMetaCountryCodeKey, metadata.countryCode)
	addFormSubmissionSystemMetadataValue(data, formSubmissionMetaTimeZoneKey, metadata.timeZone)
	merged, _ := json.Marshal(data)
	return merged
}

func validateFormSubmissionDataPayload(rawData []byte) error {
	var data map[string]json.RawMessage
	if err := json.Unmarshal(rawData, &data); err != nil || data == nil {
		return domainerr.InvalidField("data", "must be a JSON object")
	}
	for key := range data {
		if isReservedFormSubmissionDataKey(key) {
			return domainerr.InvalidField(key, "reserved for server-managed form submission metadata")
		}
	}
	return nil
}

func isReservedFormSubmissionDataKey(key string) bool {
	return strings.HasPrefix(key, formSubmissionSystemMetadataKeyPrefix)
}

func addFormSubmissionSystemMetadataValue(data structured.Fields, key string, value *string) {
	if value != nil {
		data[key] = *value
	}
}
