package telemetry

import (
	"encoding/json"
	"time"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// These explicit DTOs are the only persistence serialization boundary. They
// preserve typed shared records without a generic reflection/map serializer.
type domainAuditPersistenceRecord struct {
	AuditID       string
	OccurredAt    time.Time
	Action        sharedtelemetry.AuditAction
	ActorKind     string
	ActorMemberID string
	ActorService  string
	RequestID     string
	TraceID       string
	SpanID        string
	TargetType    string
	TargetID      string
	Attributes    []byte
}

type domainAuditAttributes struct {
	AssetID              string                                   `json:"asset_id,omitempty"`
	AssetSlot            sharedtelemetry.AuditAssetSlot           `json:"asset_slot,omitempty"`
	ConsentID            string                                   `json:"consent_id,omitempty"`
	Nickname             string                                   `json:"nickname,omitempty"`
	ChangedFields        []string                                 `json:"changed_fields,omitempty"`
	CollectionOperation  sharedtelemetry.AuditCollectionOperation `json:"collection_operation,omitempty"`
	ItemOperation        sharedtelemetry.AuditItemOperation       `json:"item_operation,omitempty"`
	PreviousState        sharedtelemetry.AuditState               `json:"previous_state,omitempty"`
	NewState             sharedtelemetry.AuditState               `json:"new_state,omitempty"`
	EffectiveAt          *time.Time                               `json:"effective_at,omitempty"`
	ScheduledAt          *time.Time                               `json:"scheduled_at,omitempty"`
	ScheduledTimeZone    string                                   `json:"scheduled_time_zone,omitempty"`
	VersionID            string                                   `json:"version_id,omitempty"`
	VersionNumber        *int64                                   `json:"version_number,omitempty"`
	ContributorMemberIDs []string                                 `json:"contributor_member_ids,omitempty"`
	EventName            string                                   `json:"event_name,omitempty"`
	FileID               string                                   `json:"file_id,omitempty"`
	FileIDs              *[]string                                `json:"file_ids,omitempty"`
	ItemID               string                                   `json:"item_id,omitempty"`
	ItemIDs              *[]string                                `json:"item_ids,omitempty"`
	ItemScope            sharedtelemetry.AuditItemScope           `json:"item_scope,omitempty"`
	ParentID             string                                   `json:"parent_id,omitempty"`
	PolicyType           sharedtelemetry.AuditPolicyType          `json:"policy_type,omitempty"`
	PostIDs              *[]string                                `json:"post_ids,omitempty"`
	PreferredLocale      string                                   `json:"preferred_locale,omitempty"`
	Locale               string                                   `json:"locale,omitempty"`
	PreviousLocale       string                                   `json:"previous_locale,omitempty"`
	NewLocale            string                                   `json:"new_locale,omitempty"`
	PreviousItemID       string                                   `json:"previous_item_id,omitempty"`
	PreviousItemIDs      *[]string                                `json:"previous_item_ids,omitempty"`
	PreviousParentID     string                                   `json:"previous_parent_id,omitempty"`
	NewParentID          string                                   `json:"new_parent_id,omitempty"`
	PreviousRelationship sharedtelemetry.AuditRelationship        `json:"previous_relationship,omitempty"`
	NewRelationship      sharedtelemetry.AuditRelationship        `json:"new_relationship,omitempty"`
	PreviousSeriesID     string                                   `json:"previous_series_id,omitempty"`
	NewSeriesID          string                                   `json:"new_series_id,omitempty"`
	SubjectMemberID      string                                   `json:"subject_member_id,omitempty"`
	SubjectPostID        string                                   `json:"subject_post_id,omitempty"`
	TagIDs               *[]string                                `json:"tag_ids,omitempty"`
	TagName              string                                   `json:"tag_name,omitempty"`
	PreviousRole         string                                   `json:"previous_role,omitempty"`
	NewRole              string                                   `json:"new_role,omitempty"`
	Email                string                                   `json:"email,omitempty"`
	PreviousEmail        string                                   `json:"previous_email,omitempty"`
	NewEmail             string                                   `json:"new_email,omitempty"`
	Provider             string                                   `json:"provider,omitempty"`
	ProviderSubject      string                                   `json:"provider_subject,omitempty"`
	PasskeyIDs           []string                                 `json:"passkey_ids,omitempty"`
	SessionScope         sharedtelemetry.AccountSessionScope      `json:"session_scope,omitempty"`
	SessionIDs           []string                                 `json:"session_ids,omitempty"`
}

func serializeDomainAudit(record sharedtelemetry.AuditRecord) (domainAuditPersistenceRecord, error) {
	attributes, err := json.Marshal(domainAuditAttributes{
		AssetID: record.AssetID, AssetSlot: record.AssetSlot, ConsentID: record.ConsentID,
		Nickname: record.Nickname, ChangedFields: record.ChangedFields,
		CollectionOperation: record.CollectionOperation, ItemOperation: record.ItemOperation,
		PreviousState: record.PreviousState, NewState: record.NewState, EffectiveAt: record.EffectiveAt,
		ScheduledAt: record.ScheduledAt, ScheduledTimeZone: record.ScheduledTimeZone, VersionID: record.VersionID,
		VersionNumber: record.VersionNumber, ContributorMemberIDs: record.ContributorMemberIDs, EventName: record.EventName,
		FileID: record.FileID, FileIDs: record.FileIDs, ItemID: record.ItemID, ItemIDs: record.ItemIDs,
		ItemScope: record.ItemScope, ParentID: record.ParentID, PolicyType: record.PolicyType, PostIDs: record.PostIDs,
		PreferredLocale: record.PreferredLocale, Locale: record.Locale,
		PreviousLocale: record.PreviousLocale, NewLocale: record.NewLocale,
		PreviousItemID: record.PreviousItemID, PreviousItemIDs: record.PreviousItemIDs,
		PreviousParentID: record.PreviousParentID, NewParentID: record.NewParentID,
		PreviousRelationship: record.PreviousRelationship, NewRelationship: record.NewRelationship,
		PreviousSeriesID: record.PreviousSeriesID, NewSeriesID: record.NewSeriesID,
		SubjectMemberID: record.SubjectMemberID, SubjectPostID: record.SubjectPostID, TagIDs: record.TagIDs,
		TagName: record.TagName, PreviousRole: record.PreviousRole, NewRole: record.NewRole, Email: record.Email,
		PreviousEmail: record.PreviousEmail, NewEmail: record.NewEmail, Provider: record.Provider,
		ProviderSubject: record.ProviderSubject, PasskeyIDs: record.PasskeyIDs, SessionScope: record.SessionScope,
		SessionIDs: record.SessionIDs,
	})
	if err != nil {
		return domainAuditPersistenceRecord{}, err
	}
	return domainAuditPersistenceRecord{
		AuditID:       record.AuditID,
		OccurredAt:    record.OccurredAt,
		Action:        record.Action,
		ActorKind:     string(record.Kind),
		ActorMemberID: record.MemberID,
		ActorService:  record.Service,
		RequestID:     record.RequestID,
		TraceID:       record.TraceID,
		SpanID:        record.SpanID,
		TargetType:    record.TargetType,
		TargetID:      record.TargetID,
		Attributes:    attributes,
	}, nil
}

type securityAccessPersistenceRecord struct {
	AccessID      string
	OccurredAt    time.Time
	Action        sharedtelemetry.SecurityAction
	ActorKind     string
	ActorMemberID string
	RequestID     string
	TraceID       string
	SpanID        string
	SourceIP      string
	Attributes    []byte
}
type securityAccessAttributes struct {
	FlowKind             sharedtelemetry.AuthenticationFlowKind       `json:"flow_kind,omitempty"`
	AuthenticationMethod sharedtelemetry.AuthenticationMethod         `json:"authentication_method,omitempty"`
	PrincipalState       sharedtelemetry.AuthenticationPrincipalState `json:"principal_state,omitempty"`
	Provider             string                                       `json:"provider,omitempty"`
	Reason               string                                       `json:"reason,omitempty"`
	AttemptedAction      string                                       `json:"attempted_action,omitempty"`
	Permission           string                                       `json:"permission,omitempty"`
	SubjectType          string                                       `json:"subject_type,omitempty"`
	SubjectID            string                                       `json:"subject_id,omitempty"`
	AccessKind           sharedtelemetry.PersonalDataAccessKind       `json:"access_kind,omitempty"`
	DataCategory         string                                       `json:"data_category,omitempty"`
}

func serializeSecurityAccess(record sharedtelemetry.SecurityAccessRecord) (securityAccessPersistenceRecord, error) {
	attributes, err := json.Marshal(securityAccessAttributes{
		FlowKind:             record.FlowKind,
		AuthenticationMethod: record.AuthenticationMethod,
		PrincipalState:       record.PrincipalState,
		Provider:             record.Provider,
		Reason:               record.Reason,
		AttemptedAction:      record.AttemptedAction,
		Permission:           record.Permission,
		SubjectType:          record.SubjectType,
		SubjectID:            record.SubjectID,
		AccessKind:           record.AccessKind,
		DataCategory:         record.DataCategory,
	})
	if err != nil {
		return securityAccessPersistenceRecord{}, err
	}
	return securityAccessPersistenceRecord{
		AccessID:      record.AccessID,
		OccurredAt:    record.OccurredAt,
		Action:        record.Action,
		ActorKind:     string(record.Kind),
		ActorMemberID: record.MemberID,
		RequestID:     record.RequestID,
		TraceID:       record.TraceID,
		SpanID:        record.SpanID,
		SourceIP:      record.SourceIP,
		Attributes:    attributes,
	}, nil
}
