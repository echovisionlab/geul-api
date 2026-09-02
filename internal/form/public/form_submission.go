package public

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/geoip"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/requestip"
	"github.com/echovisionlab/geul-api/internal/structured"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
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

// Submit validates access and stores one form submission atomically.
func (s *FormService) Submit(
	ctx context.Context,
	req *connect.Request[openv1.SubmitFormRequest],
) (*connect.Response[openv1.SubmitFormResponse], error) {
	if err := validateFormSubmissionDataPayload(req.Msg.Data); err != nil {
		return nil, err
	}
	memberID := authenticatedFormMemberID(ctx)
	metadata, err := s.resolveFormSubmissionMetadata(ctx, req, memberID)
	if err != nil {
		return nil, err
	}

	var submission model.FormSubmission
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		created, err := s.createFormSubmissionWithDB(ctx, tx, req.Msg, memberID, metadata)
		submission = created
		if err != nil {
			return err
		}
		return s.appendFormSubmissionCreatedAudit(ctx, tx, submission.ID, submission.FormID, memberID)
	})
	if err != nil {
		return nil, normalizeFormSubmissionError(err)
	}
	return connect.NewResponse(&openv1.SubmitFormResponse{SubmissionId: submission.ID}), nil
}

func (s *FormService) appendFormSubmissionCreatedAudit(
	ctx context.Context,
	tx *gorm.DB,
	submissionID, formID string,
	memberID *string,
) error {
	if s.auditWriter == nil {
		return nil
	}
	actor := sharedtelemetry.RecordActor{Kind: sharedtelemetry.ActorKindAnonymous}
	if memberID != nil {
		resolved, err := sharedtelemetry.ActorForRecord(sharedtelemetry.MemberActor{MemberID: *memberID})
		if err != nil {
			return err
		}
		actor = resolved
	}
	record, err := sharedtelemetry.NewFormSubmissionCreatedAuditRecord(sharedtelemetry.AuditMetadata{
		AuditID: uuid.NewString(), OccurredAt: time.Now().UTC(),
		Correlation: sharedtelemetry.CorrelationFromContext(ctx), RecordActor: actor,
	}, submissionID, formID)
	if err != nil {
		return err
	}
	return s.auditWriter.AppendDomainAuditInTransaction(ctx, tx, record)
}

func authenticatedFormMemberID(ctx context.Context) *string {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.MemberID == "" {
		return nil
	}
	memberID := user.MemberID.String()
	return &memberID
}

func (s *FormService) resolveFormSubmissionMetadata(
	ctx context.Context,
	req connect.AnyRequest,
	memberID *string,
) (formSubmissionSystemMetadata, error) {
	metadata := buildFormSubmissionSystemMetadata(ctx, req)
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

func (s *FormService) loadFormMemberPreferredLocale(ctx context.Context, memberID string) (*string, error) {
	var member model.Member
	err := s.db.WithContext(ctx).
		Select("preferred_locale").
		Where("id = ?", memberID).
		Take(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, errs.Internal(err)
	}
	if member.PreferredLocale == nil {
		return nil, nil
	}
	return localization.NormalizeSupportedLocale(*member.PreferredLocale), nil
}

func (s *FormService) createFormSubmissionWithDB(
	ctx context.Context,
	tx *gorm.DB,
	request *openv1.SubmitFormRequest,
	memberID *string,
	metadata formSubmissionSystemMetadata,
) (model.FormSubmission, error) {
	form, err := lockFormForSubmission(tx, request.FormId)
	if err != nil {
		return model.FormSubmission{}, err
	}
	if err := s.enforceLockedFormSubmissionAccess(ctx, &form, request.Password); err != nil {
		return model.FormSubmission{}, err
	}
	data, err := s.validateAndEnrichFormSubmission(ctx, tx, form.ID, request.Data, metadata)
	if err != nil {
		return model.FormSubmission{}, err
	}
	if err := enforceFormSubmissionLimits(tx, &form, memberID); err != nil {
		return model.FormSubmission{}, err
	}
	submission := model.FormSubmission{
		FormID:      form.ID,
		MemberID:    memberID,
		Data:        data,
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
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&form, "id = ?", formID).Error
	return form, err
}

func (s *FormService) enforceLockedFormSubmissionAccess(
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

func (s *FormService) validateAndEnrichFormSubmission(
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
	if err := formdomain.ValidateFormSubmissionAgainstSchema(sourceDocument.Schema, data); err != nil {
		return nil, errs.InvalidArgument("data", err.Error())
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
		return errs.FailedPrecondition("submission limit reached")
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
		return errs.FailedPrecondition(errs.MsgFormAlreadySubmitted)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}

func normalizeFormSubmissionError(err error) error {
	if connectErr, ok := err.(*connect.Error); ok {
		return connectErr
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errs.NotFoundMsg("form not found")
	}
	return errs.Internal(err)
}

func buildFormSubmissionSystemMetadata(
	ctx context.Context,
	req connect.AnyRequest,
) formSubmissionSystemMetadata {
	metadata := formSubmissionSystemMetadata{ipAddress: extractFormSubmissionClientIP(req)}
	if userAgent := strings.TrimSpace(req.Header().Get("User-Agent")); userAgent != "" {
		metadata.userAgent = &userAgent
	}
	metadata.locale = localization.InferPreferredLocaleFromAcceptLanguage(req.Header().Get("Accept-Language"))
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

func extractFormSubmissionClientIP(req connect.AnyRequest) *string {
	ip := requestip.TrustedClientIP(
		req.Header().Get("X-Forwarded-For"),
		req.Header().Get("X-Real-IP"),
		req.Peer().Addr,
	)
	if ip == "" {
		return nil
	}
	return &ip
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

func validateFormSubmissionDataPayload(rawData []byte) *connect.Error {
	var data map[string]json.RawMessage
	if err := json.Unmarshal(rawData, &data); err != nil || data == nil {
		return errs.InvalidArgument("data", "must be a JSON object")
	}
	for key := range data {
		if isReservedFormSubmissionDataKey(key) {
			return errs.InvalidArgument(key, "reserved for server-managed form submission metadata")
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
