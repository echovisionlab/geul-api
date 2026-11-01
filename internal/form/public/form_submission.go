package public

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/geoip"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
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

// SubmitForm validates access and stores one form submission atomically.
func (s *Service) SubmitForm(ctx context.Context, command SubmitFormCommand) (*openv1.SubmitFormResponse, error) {
	if err := validateFormSubmissionDataPayload(command.Data); err != nil {
		return nil, err
	}
	memberID := authenticatedFormMemberID(ctx)
	metadata, err := s.resolveFormSubmissionMetadata(ctx, command, memberID)
	if err != nil {
		return nil, err
	}
	var form model.Form
	submission, err := mutation.Run(ctx, s.mutations, mutation.Spec[model.FormSubmission]{
		Lock: func(_ context.Context, tx *gorm.DB) error {
			var err error
			form, err = lockFormForSubmission(tx, command.FormID)
			return err
		},
		// A visitor needs no session: the Form's own access policy, read
		// from the locked row, admits the submission.
		Access: mutation.Checked(func(ctx context.Context, _ *gorm.DB) error {
			return s.enforceLockedFormSubmissionAccess(ctx, &form, command.Password)
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (model.FormSubmission, error) {
			return s.createFormSubmission(ctx, tx, &form, command.Data, memberID, metadata)
		},
		Audit: func(submission model.FormSubmission) mutation.Audit {
			return formSubmissionCreatedAudit(submission, memberID)
		},
	})
	if err != nil {
		return nil, normalizeFormSubmissionError(err)
	}
	return &openv1.SubmitFormResponse{SubmissionId: submission.ID}, nil
}

// formSubmissionCreatedAudit attributes the submission to its Member, or to
// an anonymous visitor.
func formSubmissionCreatedAudit(submission model.FormSubmission, memberID *string) mutation.Audit {
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
	preferred, err := s.members.PreferredLocale(ctx, s.db, memberID)
	if err != nil || preferred == nil {
		return nil, err
	}
	return localization.NormalizeSupportedLocale(*preferred), nil
}

func (s *Service) createFormSubmission(
	ctx context.Context,
	tx *gorm.DB,
	form *model.Form,
	data []byte,
	memberID *string,
	metadata formSubmissionSystemMetadata,
) (model.FormSubmission, error) {
	enriched, err := s.validateAndEnrichFormSubmission(ctx, tx, form.ID, data, metadata)
	if err != nil {
		return model.FormSubmission{}, err
	}
	if err := enforceFormSubmissionLimits(tx, form, memberID); err != nil {
		return model.FormSubmission{}, err
	}
	submission := model.FormSubmission{
		FormID:      form.ID,
		MemberID:    memberID,
		Data:        enriched,
		IPAddress:   metadata.ipAddress,
		CountryCode: metadata.countryCode,
		UserAgent:   metadata.userAgent,
		CreatedAt:   time.Now(),
	}
	if err := tx.Omit("ID").Clauses(clause.Returning{}).Create(&submission).Error; err != nil {
		return model.FormSubmission{}, err
	}
	return submission, nil
}

func lockFormForSubmission(tx *gorm.DB, formID string) (model.Form, error) {
	var form model.Form
	err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&form, "id = ?", formID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return form, errFormNotFound()
	}
	return form, err
}

func (s *Service) enforceLockedFormSubmissionAccess(
	ctx context.Context,
	form *model.Form,
	password *string,
) error {
	return s.enforceFormAccess(ctx, form, formAccessOptions{
		context:                  openv1.FormAccessContext_FORM_ACCESS_CONTEXT_EMBED,
		hasValidPreviewToken:     false,
		draftAsNotFound:          false,
		enforcePassword:          true,
		password:                 password,
		checkSubmissionLimit:     false,
		checkDuplicateSubmission: false,
	})
}

func (s *Service) validateAndEnrichFormSubmission(
	ctx context.Context,
	tx *gorm.DB,
	formID string,
	data []byte,
	metadata formSubmissionSystemMetadata,
) ([]byte, error) {
	sourceDocument, err := formdomain.LoadCurrentFormSourceDocument(
		ctx, tx, formID,
	)
	if err != nil {
		return nil, err
	}
	if err := formdomain.ValidateFormSubmissionAgainstSchema(ctx, sourceDocument.Schema, data); err != nil {
		return nil, domainerr.InvalidField("data", err.Error())
	}
	return mergeValidatedFormSubmissionSystemMetadata(data, metadata), nil
}

func enforceFormSubmissionLimits(tx *gorm.DB, form *model.Form, memberID *string) error {
	if err := enforceFormSubmissionCapacity(tx, form); err != nil {
		return err
	}
	return enforceDuplicateFormSubmissionPolicy(tx, form, memberID)
}

func enforceFormSubmissionCapacity(tx *gorm.DB, form *model.Form) error {
	if form.MaxSubmissions == nil {
		return nil
	}
	var count int64
	if err := tx.Model(&model.FormSubmission{}).Where("form_id = ?", form.ID).Count(&count).Error; err != nil {
		return err
	}
	if count >= int64(*form.MaxSubmissions) {
		return domainerr.Precondition("submission limit reached")
	}
	return nil
}

func enforceDuplicateFormSubmissionPolicy(tx *gorm.DB, form *model.Form, memberID *string) error {
	if form.AllowDuplicateSubmission == nil || *form.AllowDuplicateSubmission || memberID == nil {
		return nil
	}
	var existing model.FormSubmission
	err := tx.Where("form_id = ? AND member_id = ?", form.ID, *memberID).First(&existing).Error
	if err == nil {
		return domainerr.Precondition(msgFormAlreadySubmitted)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}

func normalizeFormSubmissionError(err error) error {
	if !mutation.IsClassified(err) && errors.Is(err, gorm.ErrRecordNotFound) {
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
