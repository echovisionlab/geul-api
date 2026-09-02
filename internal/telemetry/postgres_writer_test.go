package telemetry

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPersistenceAttributeDTOsCoverTheSharedWireExactlyOnce(t *testing.T) {
	t.Parallel()
	assertAttributeTagsMatch(t,
		reflect.TypeFor[sharedtelemetry.AuditRecord](),
		reflect.TypeFor[domainAuditAttributes](),
		map[string]struct{}{
			"audit_id": {}, "occurred_at": {}, "action": {},
			"request_id": {}, "trace_id": {}, "span_id": {},
			"actor_kind": {}, "actor_member_id": {}, "actor_service": {},
			"target_type": {}, "target_id": {},
		},
	)
	assertAttributeTagsMatch(t,
		reflect.TypeFor[sharedtelemetry.SecurityAccessRecord](),
		reflect.TypeFor[securityAccessAttributes](),
		map[string]struct{}{
			"access_id": {}, "occurred_at": {}, "action": {},
			"request_id": {}, "trace_id": {}, "span_id": {},
			"actor_kind": {}, "actor_member_id": {}, "actor_service": {}, "source_ip": {},
		},
	)
}

func assertAttributeTagsMatch(t *testing.T, source, attributes reflect.Type, envelope map[string]struct{}) {
	t.Helper()
	expected := jsonTags(source, true)
	actual := jsonTags(attributes, false)
	for tag := range envelope {
		delete(expected, tag)
	}
	require.Equal(t, sortedTags(expected), sortedTags(actual))
}

func jsonTags(record reflect.Type, descendAnonymous bool) map[string]int {
	tags := make(map[string]int)
	for index := range record.NumField() {
		field := record.Field(index)
		if field.Anonymous && descendAnonymous {
			for tag, count := range jsonTags(field.Type, true) {
				tags[tag] += count
			}
			continue
		}
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			tags[tag]++
		}
	}
	return tags
}

func sortedTags(tags map[string]int) []string {
	result := make([]string, 0, len(tags))
	for tag, count := range tags {
		if count == 1 {
			result = append(result, tag)
		} else {
			result = append(result, tag+" (duplicate)")
		}
	}
	sort.Strings(result)
	return result
}

func TestSerializeDomainAuditKeepsTypedAttributesAndIntentionalEmptyArrays(t *testing.T) {
	t.Parallel()
	empty := []string{}
	record := sharedtelemetry.AuditRecord{
		AuditID: "audit-1", OccurredAt: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC),
		Action:      sharedtelemetry.AuditPostUpdated,
		RecordActor: sharedtelemetry.RecordActor{Kind: sharedtelemetry.ActorKindMember, MemberID: "member-1"},
		TargetType:  "post", TargetID: "post-1",
		ChangedFields: []string{"authors"}, SubjectMemberID: "member-2",
		PreviousRelationship: sharedtelemetry.AuditRelationshipNone,
		NewRelationship:      sharedtelemetry.AuditRelationshipAuthor,
		FileIDs:              &empty, ItemIDs: &empty, PostIDs: &empty, TagIDs: &empty,
	}
	persisted, err := serializeDomainAudit(record)
	require.NoError(t, err)
	require.Equal(t, "audit-1", persisted.AuditID)
	require.JSONEq(t, `{
		"changed_fields":["authors"],
		"subject_member_id":"member-2",
		"previous_relationship":"none",
		"new_relationship":"author",
		"file_ids":[],"item_ids":[],"post_ids":[],"tag_ids":[]
	}`, string(persisted.Attributes))
}

func TestSerializeDomainAuditMapsEverySharedAttributeValue(t *testing.T) {
	t.Parallel()
	effectiveAt := time.Date(2026, 8, 12, 1, 2, 3, 4, time.UTC)
	scheduledAt := effectiveAt.Add(37 * time.Minute)
	versionNumber := int64(43)
	fileIDs := []string{"file-list-a", "file-list-b"}
	itemIDs := []string{"item-list-a", "item-list-b"}
	postIDs := []string{"post-list-a", "post-list-b"}
	previousItemIDs := []string{"previous-item-list-a", "previous-item-list-b"}
	tagIDs := []string{"tag-list-a", "tag-list-b"}
	record := sharedtelemetry.AuditRecord{
		AuditID: "00000000-0000-4000-8000-000000000001", OccurredAt: effectiveAt,
		Action: sharedtelemetry.AuditPostUpdated,
		Correlation: sharedtelemetry.Correlation{
			RequestID: "00000000-0000-4000-8000-000000000002",
			TraceID:   "11111111111111111111111111111111",
			SpanID:    "2222222222222222",
		},
		RecordActor: sharedtelemetry.RecordActor{Kind: sharedtelemetry.ActorKindSystem, Service: "mapping-service"},
		TargetType:  "post", TargetID: "post-target",
		AssetID: "asset-value", AssetSlot: sharedtelemetry.AuditAssetSlotDark,
		ConsentID: "consent-value", Nickname: "nickname-value",
		ChangedFields: []string{"alpha-field", "beta-field"}, CollectionOperation: sharedtelemetry.AuditCollectionOperationAdded,
		ItemOperation: sharedtelemetry.AuditItemOperationUpdated,
		PreviousState: sharedtelemetry.AuditStateDraft, NewState: sharedtelemetry.AuditStatePublished,
		EffectiveAt: &effectiveAt, ScheduledAt: &scheduledAt, ScheduledTimeZone: "Asia/Seoul",
		VersionID: "version-value", VersionNumber: &versionNumber,
		ContributorMemberIDs: []string{"contributor-a", "contributor-b"}, EventName: "event-value",
		FileID: "file-value", FileIDs: &fileIDs, ItemID: "item-value", ItemIDs: &itemIDs,
		ItemScope: sharedtelemetry.AuditItemScopeDashboard, ParentID: "parent-value",
		PolicyType: sharedtelemetry.AuditPolicyTypePrivacy, PostIDs: &postIDs,
		PreferredLocale: "ko", Locale: "ja", PreviousLocale: "en", NewLocale: "ko",
		PreviousItemID: "previous-item-value", PreviousItemIDs: &previousItemIDs,
		PreviousParentID: "previous-parent-value", NewParentID: "new-parent-value",
		PreviousRelationship: sharedtelemetry.AuditRelationshipNone,
		NewRelationship:      sharedtelemetry.AuditRelationshipManager,
		PreviousSeriesID:     "previous-series-value", NewSeriesID: "new-series-value",
		SubjectMemberID: "subject-member-value", SubjectPostID: "subject-post-value",
		TagIDs: &tagIDs, TagName: "tag-name-value", PreviousRole: "previous-role-value", NewRole: "new-role-value",
		Email: "email@example.test", PreviousEmail: "previous@example.test", NewEmail: "new@example.test",
		Provider: "provider-value", ProviderSubject: "provider-subject-value",
		PasskeyIDs: []string{"passkey-a", "passkey-b"}, SessionScope: sharedtelemetry.AccountSessionScopeOthers,
		SessionIDs: []string{"session-a", "session-b"},
	}
	persisted, err := serializeDomainAudit(record)
	require.NoError(t, err)
	assertSerializedAttributesMatchFlatRecord(t, record, persisted.Attributes, []string{
		"audit_id", "occurred_at", "action", "request_id", "trace_id", "span_id",
		"actor_kind", "actor_member_id", "actor_service", "target_type", "target_id",
	})
}

func TestSerializeSecurityAccessKeepsTypedAttributes(t *testing.T) {
	t.Parallel()
	record := sharedtelemetry.SecurityAccessRecord{
		AccessID: "access-1", OccurredAt: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC),
		Action:      sharedtelemetry.SecurityAuthorizationDenied,
		RecordActor: sharedtelemetry.RecordActor{Kind: sharedtelemetry.ActorKindAnonymous},
		Correlation: sharedtelemetry.Correlation{RequestID: "request-1"}, SourceIP: "192.0.2.1",
		AttemptedAction: "/test.v1.PrivateService/GetSecret", Permission: sharedtelemetry.AuthorizationProcedureInvokePermission,
		Reason: string(sharedtelemetry.AuthorizationDeniedAuthenticationRequired),
	}
	persisted, err := serializeSecurityAccess(record)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"attempted_action":"/test.v1.PrivateService/GetSecret",
		"permission":"procedure:invoke",
		"reason":"authentication_required"
	}`, string(persisted.Attributes))
}

func TestSerializeSecurityAccessMapsEverySharedAttributeValue(t *testing.T) {
	t.Parallel()
	record := sharedtelemetry.SecurityAccessRecord{
		AccessID:   "00000000-0000-4000-8000-000000000003",
		OccurredAt: time.Date(2026, 8, 12, 2, 3, 4, 5, time.UTC),
		Action:     sharedtelemetry.SecurityPersonalDataAccessed,
		Correlation: sharedtelemetry.Correlation{
			RequestID: "00000000-0000-4000-8000-000000000004",
			TraceID:   "33333333333333333333333333333333",
			SpanID:    "4444444444444444",
		},
		RecordActor:          sharedtelemetry.RecordActor{Kind: sharedtelemetry.ActorKindSystem, Service: "security-mapping-service"},
		SourceIP:             "2001:db8::7",
		FlowKind:             sharedtelemetry.AuthenticationFlowRegistration,
		AuthenticationMethod: sharedtelemetry.AuthenticationMethodPasskey,
		PrincipalState:       sharedtelemetry.AuthenticationPrincipalOnboardingOnly,
		Provider:             "security-provider-value", Reason: "security-reason-value",
		AttemptedAction: "security-attempted-action-value", Permission: "security-permission-value",
		SubjectType: "security-subject-type-value", SubjectID: "security-subject-id-value",
		AccessKind: sharedtelemetry.PersonalDataAccessRead, DataCategory: "security-data-category-value",
	}
	persisted, err := serializeSecurityAccess(record)
	require.NoError(t, err)
	assertSerializedAttributesMatchFlatRecord(t, record, persisted.Attributes, []string{
		"access_id", "occurred_at", "action", "request_id", "trace_id", "span_id",
		"actor_kind", "actor_member_id", "actor_service", "source_ip",
	})
}

func assertSerializedAttributesMatchFlatRecord(t *testing.T, record any, attributes []byte, envelope []string) {
	t.Helper()
	flatJSON, err := json.Marshal(record)
	require.NoError(t, err)
	var expected map[string]any
	require.NoError(t, json.Unmarshal(flatJSON, &expected))
	for _, key := range envelope {
		delete(expected, key)
	}
	var actual map[string]any
	require.NoError(t, json.Unmarshal(attributes, &actual))
	require.Equal(t, expected, actual)
}

func TestDurableWriterRejectsInvalidRecordsBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()

	writer := NewDurableWriter(nil)
	invalidAudit := sharedtelemetry.AuditRecord{Action: sharedtelemetry.AuditPostCreated}
	require.ErrorContains(t, writer.AppendDomainAuditInTransaction(context.Background(), nil, invalidAudit), "validate domain audit")

	invalidAccess := sharedtelemetry.SecurityAccessRecord{Action: sharedtelemetry.SecurityAuthenticationSucceeded}
	require.ErrorContains(t, writer.AppendSecurityAccess(context.Background(), invalidAccess), "validate security access")
}

func TestDurableWriterRequiresPersistenceBoundariesForValidRecords(t *testing.T) {
	t.Parallel()

	memberID := uuid.NewString()
	requestID := uuid.NewString()
	actor := sharedtelemetry.RecordActor{Kind: sharedtelemetry.ActorKindMember, MemberID: memberID}
	audit, err := sharedtelemetry.NewPostCreatedAuditRecord(sharedtelemetry.AuditMetadata{
		AuditID: uuid.NewString(), OccurredAt: time.Now().UTC(),
		Correlation: sharedtelemetry.Correlation{RequestID: requestID}, RecordActor: actor,
	}, uuid.NewString())
	require.NoError(t, err)
	access, err := sharedtelemetry.NewAuthenticationSucceededRecord(sharedtelemetry.SecurityAccessMetadata{
		AccessID: uuid.NewString(), OccurredAt: time.Now().UTC(),
		Correlation: sharedtelemetry.Correlation{RequestID: requestID}, RecordActor: actor, SourceIP: "192.0.2.1",
	}, sharedtelemetry.AuthenticationContext{
		FlowKind: sharedtelemetry.AuthenticationFlowLogin, AuthenticationMethod: sharedtelemetry.AuthenticationMethodEmailCode,
		PrincipalState: sharedtelemetry.AuthenticationPrincipalActive,
	})
	require.NoError(t, err)

	writer := NewDurableWriter(nil)
	require.ErrorContains(t, writer.AppendDomainAuditInTransaction(context.Background(), nil, audit), "transaction is required")
	require.ErrorContains(t, writer.AppendSecurityAccess(context.Background(), access), "database is required")
}
