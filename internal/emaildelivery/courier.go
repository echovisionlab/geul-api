package emaildelivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

var errAuthCodeIssuanceProvenanceMissing = errors.New("auth issuance provenance is missing")

const authIssuanceProvenanceNamespace = "__geul_auth_issuance"

// EmailCourierService implements the EmailCourierService Connect handler
type EmailCourierService struct {
	intrav1connect.UnimplementedEmailCourierServiceHandler
	publisher         EmailCourierPublisher
	kratosClient      auth.IdentityManager
	issuanceAuthority AuthIssuanceAuthority
	codeLifespan      time.Duration
	now               func() time.Time
}

type AuthIssuance struct {
	IssuanceID string
	IssuedAt   time.Time
}

type AuthIssuanceProvenance struct {
	Version    string
	IssuanceID string
	IssuedAt   string
	Purpose    string
	Recipient  string
	MAC        string
}

// AuthIssuanceAuthority verifies signed issuance proof and owns the narrow
// settings-generated verification reservation recovery path.
type AuthIssuanceAuthority interface {
	Verify(email.EventKey, string, AuthIssuanceProvenance) (AuthIssuance, error)
	RestoreSettingsVerification(context.Context, email.EventKey, string, string) (AuthIssuance, bool, error)
	IdempotencyKey(email.EventKey, string, string) (string, error)
}

type EmailCourierPublisher interface {
	PublishAuthEmail(context.Context, *managev1.SendEmailEvent) error
}

// NewEmailCourierService creates a new EmailCourierService
func NewEmailCourierService(
	publisher EmailCourierPublisher,
	kratosClient auth.IdentityManager,
	issuanceAuthority AuthIssuanceAuthority,
	codeLifespan time.Duration,
) *EmailCourierService {
	if publisher == nil {
		panic("email courier publisher is required")
	}
	if issuanceAuthority == nil {
		panic("email courier issuance authority is required")
	}
	if codeLifespan <= 0 {
		panic("email courier code lifespan must be positive")
	}
	return &EmailCourierService{
		publisher:         publisher,
		kratosClient:      kratosClient,
		issuanceAuthority: issuanceAuthority,
		codeLifespan:      codeLifespan,
		now:               time.Now,
	}
}

// SendEmail receives email requests from Kratos HTTP courier and queues them.
// The HTTP route is additionally protected by the internal-service credential;
// private-network placement is defense in depth.
func (s *EmailCourierService) SendEmail(
	ctx context.Context,
	req *connect.Request[intrav1.SendEmailRequest],
) (*connect.Response[intrav1.SendEmailResponse], error) {
	slog.Info("Received email request from Kratos",
		"template_type", req.Msg.TemplateType,
	)

	// Validate required fields
	if req.Msg.Recipient == "" {
		return nil, errs.Required("recipient")
	}
	if req.Msg.TemplateType == "" {
		return nil, errs.Required("template_type")
	}

	// Convert template_data from Struct to map[string]string
	templateData := make(map[string]string)
	if req.Msg.TemplateData != nil {
		templateData = structToStringMap(req.Msg.TemplateData)
	}
	identityID := identityIDFromTemplateData(req.Msg.TemplateData)
	targetLocale := localeFromTemplateData(req.Msg.TemplateData)

	// Interpret provider-specific courier input only at this boundary. Internal
	// queue jobs always carry a canonical provider-neutral event key.
	templateEvent, supported := normalizeIdentityCourierTemplateType(req.Msg.TemplateType)
	if !supported {
		reason := "unsupported_template_type"
		switch req.Msg.TemplateType {
		case "verification_code_invalid":
			reason = "disabled_anti_enumeration_selector"
		}
		slog.Info("Skipped unsupported identity courier email",
			"template_type", req.Msg.TemplateType,
			"reason", reason,
		)
		return connect.NewResponse(&intrav1.SendEmailResponse{Queued: false}), nil
	}
	templateType := templateEvent.String()
	templateData = canonicalAutomaticEmailTemplateData(templateEvent, templateData)
	issuance, err := s.requireAuthIssuanceProvenance(
		ctx,
		templateEvent,
		req.Msg.Recipient,
		identityID,
		req.Msg.TemplateData,
	)
	if err != nil {
		slog.Warn("Rejected identity courier email with invalid issuance provenance",
			"template_type", req.Msg.TemplateType,
			"reason", "invalid_issuance_provenance",
		)
		return nil, errs.PermissionDenied("authentication issuance provenance is invalid")
	}

	// Build SendEmailEvent proto with template_type and template_data
	job := &managev1.SendEmailEvent{
		Recipient:    req.Msg.Recipient,
		TemplateType: templateType,
		TemplateData: templateData,
	}
	if targetLocale != "" {
		job.Locale = &targetLocale
	}
	if ok := applyAuthRecipientContext(job, templateType, req.Msg.Recipient, identityID); !ok {
		slog.Warn("Skipped identity courier email without required authority context",
			"template_type", req.Msg.TemplateType,
		)
		return connect.NewResponse(&intrav1.SendEmailResponse{Queued: false}), nil
	}
	if templateType == email.EventLoginCode.String() {
		allowed, reason, err := s.authLoginAllowed(ctx, identityID, req.Msg.Recipient)
		if err != nil {
			slog.Error("Failed to validate login-code request", "reason", "identity_lookup_failed", "identity_id", identityID)
			return nil, errs.Internal(err)
		}
		if !allowed {
			slog.Warn("Skipped login-code email before queue",
				"identity_id", identityID,
				"reason", reason,
			)
			return connect.NewResponse(&intrav1.SendEmailResponse{Queued: false}), nil
		}
	}

	codeKey := templateEvent.String()
	if strings.TrimSpace(templateData[codeKey]) == "" {
		slog.Error("Rejected identity courier email without a generated code",
			"template_type", req.Msg.TemplateType,
		)
		return nil, errs.InvalidArgument(codeKey, "generated authentication code is required")
	}
	expiresAt, err := authCodeExpiry(issuance.IssuedAt, s.codeLifespan, templateData)
	if err != nil {
		return nil, errs.InvalidArgument("expires_in_minutes", err.Error())
	}
	if !expiresAt.After(s.now().UTC()) {
		return nil, errs.InvalidArgumentMsg("authentication issuance has expired")
	}
	messageID, err := s.issuanceAuthority.IdempotencyKey(
		templateEvent,
		req.Msg.Recipient,
		issuance.IssuanceID,
	)
	if err != nil {
		return nil, errs.Internal(err)
	}
	issuanceID := issuance.IssuanceID
	job.IssuanceId = &issuanceID
	job.ExpiresAt = timestamppb.New(expiresAt)
	job.MessageId = &messageID

	err = s.publisher.PublishAuthEmail(ctx, job)
	if err != nil {
		slog.Error("Failed to publish authentication email",
			"template_type", req.Msg.TemplateType,
			"reason", "queue_publish_failed",
		)
		return nil, errs.Internal(err)
	}

	slog.Debug("Authentication email command confirmed", "message_id", messageID, "issuance_id", issuanceID)

	slog.Info("Email queued successfully", "template_type", req.Msg.TemplateType)

	return connect.NewResponse(&intrav1.SendEmailResponse{
		Queued: true,
	}), nil
}

func canonicalAutomaticEmailTemplateData(
	eventKey email.EventKey,
	data map[string]string,
) map[string]string {
	allowed := map[string]struct{}{}
	for _, name := range identityCourierTemplateVariableNames(eventKey) {
		allowed[name] = struct{}{}
	}
	canonical := make(map[string]string, len(data))
	for key, value := range data {
		key = strings.ToLower(strings.TrimSpace(key))
		if _, ok := allowed[key]; ok {
			canonical[key] = value
		}
	}
	return canonical
}

func identityCourierTemplateVariableNames(eventKey email.EventKey) []string {
	names := []string{
		"site_name", "site_origin", "logo_email_url",
		"recipient_email", "identity_email", "to",
	}
	switch eventKey {
	case email.EventVerificationCode:
		return append(names, "verification_code", "verification_url", "expires_in_minutes")
	case email.EventLoginCode:
		return append(names, "login_code", "expires_in_minutes")
	case email.EventRegistrationCode:
		return append(names, "registration_code", "expires_in_minutes")
	default:
		return names
	}
}

func (s *EmailCourierService) requireAuthIssuanceProvenance(
	ctx context.Context,
	templateEvent email.EventKey,
	recipient string,
	identityID string,
	templateData *structpb.Struct,
) (AuthIssuance, error) {
	provenance, err := authCodeIssuanceProvenanceFromTemplateData(templateData)
	if err != nil {
		if !errors.Is(err, errAuthCodeIssuanceProvenanceMissing) {
			return AuthIssuance{}, err
		}
		return s.restoreSettingsVerificationProvenance(ctx, templateEvent, recipient, identityID)
	}
	issuance, err := s.issuanceAuthority.Verify(templateEvent, recipient, provenance)
	if err != nil {
		return AuthIssuance{}, err
	}
	if issuance.IssuedAt.After(s.now().UTC().Add(30 * time.Second)) {
		return AuthIssuance{}, fmt.Errorf("auth issuance provenance time is in the future")
	}
	return issuance, nil
}

func (s *EmailCourierService) restoreSettingsVerificationProvenance(
	ctx context.Context,
	templateEvent email.EventKey,
	recipient string,
	identityID string,
) (AuthIssuance, error) {
	if templateEvent != email.EventVerificationCode {
		return AuthIssuance{}, errAuthCodeIssuanceProvenanceMissing
	}
	issuance, found, err := s.issuanceAuthority.RestoreSettingsVerification(ctx, templateEvent, recipient, identityID)
	if err != nil {
		return AuthIssuance{}, err
	}
	if !found {
		return AuthIssuance{}, errAuthCodeIssuanceProvenanceMissing
	}
	return issuance, nil
}

func authCodeIssuanceProvenanceFromTemplateData(
	templateData *structpb.Struct,
) (AuthIssuanceProvenance, error) {
	if templateData == nil {
		return AuthIssuanceProvenance{}, errAuthCodeIssuanceProvenanceMissing
	}
	transient := templateData.Fields["transient_payload"]
	if transient == nil || transient.GetStructValue() == nil {
		return AuthIssuanceProvenance{}, errAuthCodeIssuanceProvenanceMissing
	}
	reserved := transient.GetStructValue().Fields[authIssuanceProvenanceNamespace]
	if reserved == nil || reserved.GetStructValue() == nil {
		return AuthIssuanceProvenance{}, errAuthCodeIssuanceProvenanceMissing
	}
	fields := reserved.GetStructValue()
	provenance := AuthIssuanceProvenance{
		Version:    stringFromStructPath(fields, "version"),
		IssuanceID: stringFromStructPath(fields, "issuance_id"),
		IssuedAt:   stringFromStructPath(fields, "issued_at"),
		Purpose:    stringFromStructPath(fields, "purpose"),
		Recipient:  stringFromStructPath(fields, "recipient"),
		MAC:        stringFromStructPath(fields, "mac"),
	}
	if provenance.Version == "" || provenance.IssuanceID == "" ||
		provenance.IssuedAt == "" || provenance.Purpose == "" ||
		provenance.Recipient == "" || provenance.MAC == "" {
		return AuthIssuanceProvenance{}, fmt.Errorf("auth issuance provenance is incomplete")
	}
	return provenance, nil
}

func authCodeExpiry(
	issuedAt time.Time,
	configuredLifespan time.Duration,
	templateData map[string]string,
) (time.Time, error) {
	if issuedAt.IsZero() || configuredLifespan <= 0 {
		return time.Time{}, fmt.Errorf("authentication issuance lifespan is invalid")
	}
	lifespan := configuredLifespan
	if rawMinutes := strings.TrimSpace(templateData["expires_in_minutes"]); rawMinutes != "" {
		minutes, err := strconv.ParseFloat(rawMinutes, 64)
		if err != nil || minutes <= 0 || math.IsNaN(minutes) || math.IsInf(minutes, 0) {
			return time.Time{}, fmt.Errorf("must be a positive number")
		}
		providerLifespan := time.Duration(minutes * float64(time.Minute))
		if providerLifespan <= 0 {
			return time.Time{}, fmt.Errorf("must be a positive duration")
		}
		if providerLifespan < lifespan {
			lifespan = providerLifespan
		}
	}
	return issuedAt.UTC().Add(lifespan), nil
}

func (s *EmailCourierService) authLoginAllowed(ctx context.Context, identityID, recipient string) (bool, string, error) {
	identityID = strings.TrimSpace(identityID)
	recipient = strings.TrimSpace(recipient)
	if identityID == "" {
		return false, "identity_missing", nil
	}
	if s.kratosClient == nil {
		return false, "identity_manager_missing", nil
	}

	identity, err := s.kratosClient.GetIdentityWithIncludeCredential(ctx, identityID, "code")
	if err != nil {
		return false, "identity_lookup_failed", err
	}
	if identity == nil {
		return false, "identity_missing", nil
	}
	if identity.IsBanned() {
		return false, "account_banned", nil
	}
	codeCredential, ok := identity.Credentials["code"]
	if !ok || !auth.CodeCredentialHasAddress(codeCredential, recipient) {
		return false, "code_address_mismatch", nil
	}
	return true, "", nil
}

// structToStringMap converts a protobuf Struct to a map[string]string
// Non-string values are converted to their string representation
func structToStringMap(s *structpb.Struct) map[string]string {
	if s == nil {
		return nil
	}
	result := make(map[string]string)
	for k, v := range s.Fields {
		// Provider authority and flow metadata are interpreted explicitly at
		// this ingress boundary. They must not leak into user-editable template
		// variables as empty strings.
		if k == "identity" || k == "traits" || k == "transient_payload" {
			continue
		}
		if v.GetStructValue() != nil || v.GetListValue() != nil {
			continue
		}
		result[k] = valueToString(v)
	}
	return result
}

func localeFromTemplateData(data *structpb.Struct) string {
	if data == nil {
		return ""
	}

	for _, path := range [][]string{
		{"transient_payload", "locale"},
		{"transient_payload", "preferred_locale"},
		{"traits", "preferred_locale"},
		{"identity", "traits", "preferred_locale"},
	} {
		if value := stringFromStructPath(data, path...); value != "" {
			if locale := localization.NormalizeSupportedLocale(value); locale != nil {
				return *locale
			}
		}
	}

	return ""
}

func stringFromStructPath(data *structpb.Struct, path ...string) string {
	if data == nil || len(path) == 0 {
		return ""
	}

	current := data
	for index, segment := range path {
		value, ok := current.Fields[segment]
		if !ok || value == nil {
			return ""
		}
		if index == len(path)-1 {
			return strings.TrimSpace(value.GetStringValue())
		}
		current = value.GetStructValue()
		if current == nil {
			return ""
		}
	}

	return ""
}

func identityIDFromTemplateData(data *structpb.Struct) string {
	if data == nil {
		return ""
	}
	identity, ok := data.Fields["identity"]
	if !ok || identity.GetStructValue() == nil {
		return ""
	}
	id, ok := identity.GetStructValue().Fields["id"]
	if !ok {
		return ""
	}
	return strings.TrimSpace(id.GetStringValue())
}

// valueToString converts a protobuf Value to a string
func valueToString(v *structpb.Value) string {
	if v == nil {
		return ""
	}
	switch v.Kind.(type) {
	case *structpb.Value_NullValue:
		return ""
	case *structpb.Value_NumberValue:
		return fmt.Sprintf("%v", v.GetNumberValue())
	case *structpb.Value_StringValue:
		return v.GetStringValue()
	case *structpb.Value_BoolValue:
		if v.GetBoolValue() {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

// normalizeIdentityCourierTemplateType converts supported raw identity-provider
// courier selectors into provider-neutral internal event keys. The *_code_valid
// suffix names a valid-code email template; it does not mean the recipient has
// already submitted or validated that code.
func normalizeIdentityCourierTemplateType(rawTemplateType string) (email.EventKey, bool) {
	rawToEventKey := map[string]email.EventKey{
		"verification_code_valid": email.EventVerificationCode,
		"login_code_valid":        email.EventLoginCode,
		"registration_code_valid": email.EventRegistrationCode,
	}

	eventKey, ok := rawToEventKey[rawTemplateType]
	if !ok {
		return "", false
	}
	return eventKey, true
}

func applyAuthRecipientContext(
	job *managev1.SendEmailEvent,
	templateType string,
	recipient string,
	identityID string,
) bool {
	identityID = strings.TrimSpace(identityID)
	switch templateType {
	case email.EventVerificationCode.String():
		if identityID == "" {
			return false
		}
		job.RecipientContext = email.AccountVerificationContext(identityID, recipient)
	case email.EventLoginCode.String():
		if identityID == "" {
			return false
		}
		job.RecipientContext = &managev1.SendEmailEvent_AuthLogin{
			AuthLogin: &managev1.AuthLoginRecipient{
				IdentityId:  identityID,
				TargetEmail: strings.TrimSpace(recipient),
			},
		}
	case email.EventRegistrationCode.String():
		job.RecipientContext = &managev1.SendEmailEvent_AuthRegistration{
			AuthRegistration: &managev1.AuthRegistrationRecipient{
				TargetEmail: strings.TrimSpace(recipient),
			},
		}
	}

	return true
}
