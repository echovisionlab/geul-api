//go:build integration

package label

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// The participant characterization tests pin the observable contract of
// SetLabelParticipant and RemoveLabelParticipant. The Artist package carries
// the same scenarios with the same structure; only the entity adapter at the
// top of the file differs, so both must keep passing when the two participant
// stores become one.

const (
	participantCharacterizationEntity        = "label"
	participantCharacterizationTitle         = "Label"
	participantCharacterizationArticle       = "a Label"
	participantCharacterizationAuditAction   = sharedtelemetry.AuditLabelUpdated
	participantCharacterizationOwnerTable    = "label_owner"
	participantCharacterizationManagerTable  = "label_manager"
	participantCharacterizationEntityIDField = "label_id"
)

type participantCharacterizationService struct {
	service *LabelService
}

func newParticipantCharacterizationService(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	publisher *participantCharacterizationPublisher,
) participantCharacterizationService {
	t.Helper()
	return participantCharacterizationService{service: NewAuditedLabelService(
		db, spiceDB, &fakeIdentityManager{}, &recordingArtistFileDeleter{}, publisher,
		apitelemetry.NewDurableWriter(db),
		Dependencies{
			Translation: labeladapter.NewTranslation(),
			Members:     labeladapter.NewMemberProjection(db, ""),
			Runtime:     newLabelRuntimeForTest(db, ""),
		},
		WithLabelContentBlockStore(testutil.NewCreativeContentStore(t, spiceDB)),
	)}
}

func seedParticipantCharacterizationEntity(t *testing.T, db *gorm.DB, store *contentblock.Store) string {
	t.Helper()
	entityID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		document, err := store.CreateDocument(t.Context(), tx, contentblock.CreateInput{
			Profile: creativeContentProfile, SourceLocale: "en",
		})
		if err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO label (id, status, source_locale, content_document_id, created_at, updated_at)
			VALUES (?, 'LABEL_STATUS_DRAFT', 'en', ?, ?, ?)
		`, entityID, document.Document.ID, now, now).Error
	}))
	return entityID
}

func participantCharacterizationRelationshipMutation(
	t *testing.T,
	entityID string,
	identityID string,
	role string,
) policyv1.RelationshipMutation {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	var mutation policyv1.RelationshipMutation
	switch role {
	case participantRoleOwner:
		mutation, err = policyv1.Label.TouchOwner(entityID, actor)
	case participantRoleManager:
		mutation, err = policyv1.Label.TouchManager(entityID, actor)
	default:
		t.Fatalf("unsupported participant role %q", role)
	}
	require.NoError(t, err)
	return mutation
}

func participantCharacterizationPolicyMutation(t *testing.T, entityID string) policyv1.RelationshipMutation {
	t.Helper()
	mutation, err := policyv1.Label.TouchPolicy(entityID)
	require.NoError(t, err)
	return mutation
}

func participantCharacterizationSnapshotPlan(t *testing.T, entityID string) policyv1.RelationshipSnapshotPlan {
	t.Helper()
	plan, err := policyv1.Label.Snapshot(entityID)
	require.NoError(t, err)
	return plan
}

func (s participantCharacterizationService) set(
	ctx context.Context,
	entityID string,
	memberID string,
	role string,
) (*participantCharacterizationResponse, error) {
	protoRole := managev1.LabelParticipantRole_LABEL_PARTICIPANT_ROLE_UNSPECIFIED
	switch role {
	case participantRoleOwner:
		protoRole = managev1.LabelParticipantRole_LABEL_PARTICIPANT_ROLE_OWNER
	case participantRoleManager:
		protoRole = managev1.LabelParticipantRole_LABEL_PARTICIPANT_ROLE_MANAGER
	}
	response, err := s.service.SetLabelParticipant(ctx, connect.NewRequest(&managev1.SetLabelParticipantRequest{
		LabelId: entityID, MemberId: memberID, Role: protoRole,
	}))
	if err != nil {
		return nil, err
	}
	result := &participantCharacterizationResponse{
		MemberID: response.Msg.GetMember().GetId(), Nickname: response.Msg.GetMember().GetNickname(),
		CreatedAt: response.Msg.GetCreatedAt().AsTime(), HasEffectiveAuthority: response.Msg.GetHasEffectiveAuthority(),
	}
	switch response.Msg.GetRole() {
	case managev1.LabelParticipantRole_LABEL_PARTICIPANT_ROLE_OWNER:
		result.Role = participantRoleOwner
	case managev1.LabelParticipantRole_LABEL_PARTICIPANT_ROLE_MANAGER:
		result.Role = participantRoleManager
	}
	return result, nil
}

func (s participantCharacterizationService) remove(ctx context.Context, entityID, memberID string) (bool, error) {
	response, err := s.service.RemoveLabelParticipant(ctx, connect.NewRequest(&managev1.RemoveLabelParticipantRequest{
		LabelId: entityID, MemberId: memberID,
	}))
	if err != nil {
		return false, err
	}
	return response.Msg.GetSuccess(), nil
}

// ---- Scenario body: keep identical to the Artist participant characterization test. ----

type participantCharacterizationResponse struct {
	MemberID              string
	Nickname              string
	Role                  string
	CreatedAt             time.Time
	HasEffectiveAuthority bool
}

type participantCharacterizationPublisher struct {
	mu      sync.Mutex
	signals []string
}

func (p *participantCharacterizationPublisher) record(signal string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.signals = append(p.signals, signal)
}

func (p *participantCharacterizationPublisher) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.signals...)
}

func (p *participantCharacterizationPublisher) EnqueueProtobuf(_ context.Context, topic, _ string, _ proto.Message) error {
	p.record("enqueue:" + topic)
	return nil
}

func (p *participantCharacterizationPublisher) NotifyProtobuf(_ context.Context, signal string, _ proto.Message) error {
	p.record("notify:" + signal)
	return nil
}

func (p *participantCharacterizationPublisher) NotifyProtobufWithExecutor(_ context.Context, _ eventpkg.DBTX, signal string, _ proto.Message) error {
	p.record("notify:" + signal)
	return nil
}

type participantCharacterizationActor struct {
	key        string
	identityID string
	memberID   string
	nickname   string
}

type participantCharacterizationAudit struct {
	ActorMemberID        string
	SubjectMemberID      string
	PreviousRelationship string
	NewRelationship      string
	ChangedFields        string
}

type participantCharacterizationRow struct {
	MemberID  string
	Role      string
	CreatedAt time.Time
}

type participantCharacterizationEnv struct {
	db        *gorm.DB
	spiceDB   *auth.SpiceDBClient
	store     *contentblock.Store
	service   participantCharacterizationService
	publisher *participantCharacterizationPublisher
	actors    map[string]participantCharacterizationActor
}

func newParticipantCharacterizationEnv(t *testing.T) *participantCharacterizationEnv {
	t.Helper()
	db := testutil.NewIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	publisher := &participantCharacterizationPublisher{}
	env := &participantCharacterizationEnv{
		db: db, spiceDB: stack.SpiceDBClient, store: testutil.NewCreativeContentStore(t, stack.SpiceDBClient),
		service: newParticipantCharacterizationService(t, db, stack.SpiceDBClient, publisher), publisher: publisher,
		actors: map[string]participantCharacterizationActor{},
	}
	for _, key := range []string{"admin", "ownerA", "target", "outsider"} {
		identityID := testutil.IntegrationUUID()
		nickname := "Participant " + key + " " + identityID[:8]
		memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, nickname)
		env.actors[key] = participantCharacterizationActor{key: key, identityID: identityID, memberID: memberID, nickname: nickname}
	}
	testutil.GrantIntegrationGlobalRole(t, stack.SpiceDBClient, env.actors["admin"].identityID, policyv1.Role.Admin())
	return env
}

func (env *participantCharacterizationEnv) context(t *testing.T, key string) context.Context {
	t.Helper()
	if key == "anonymous" {
		return t.Context()
	}
	actor := env.actors[key]
	return testutil.NewAuditContext(t, actor.identityID, actor.memberID)
}

// seedEntity stores an entity whose participants are exactly initial, both as
// rows and as SpiceDB relationships, the way the participant RPCs leave them.
func (env *participantCharacterizationEnv) seedEntity(t *testing.T, initial map[string]string) string {
	t.Helper()
	entityID := seedParticipantCharacterizationEntity(t, env.db, env.store)
	mutations := []policyv1.RelationshipMutation{participantCharacterizationPolicyMutation(t, entityID)}
	keys := make([]string, 0, len(initial))
	for key := range initial {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for index, key := range keys {
		role := initial[key]
		actor := env.actors[key]
		table := participantCharacterizationManagerTable
		if role == participantRoleOwner {
			table = participantCharacterizationOwnerTable
		}
		createdAt := time.Now().UTC().Add(time.Duration(index-len(keys)) * time.Minute).Truncate(time.Microsecond)
		require.NoError(t, env.db.Exec(
			"INSERT INTO "+table+" ("+participantCharacterizationEntityIDField+", member_id, created_at) VALUES (?::uuid, ?::uuid, ?)",
			entityID, actor.memberID, createdAt,
		).Error)
		mutations = append(mutations, participantCharacterizationRelationshipMutation(t, entityID, actor.identityID, role))
	}
	_, err := env.spiceDB.ApplyRelationships(t.Context(), mutations...)
	require.NoError(t, err)
	return entityID
}

func (env *participantCharacterizationEnv) rows(t *testing.T, entityID string) map[string]participantCharacterizationRow {
	t.Helper()
	var rows []participantCharacterizationRow
	require.NoError(t, env.db.Raw(`
		SELECT member_id::text AS member_id, 'owner' AS role, created_at FROM `+participantCharacterizationOwnerTable+` WHERE `+participantCharacterizationEntityIDField+` = ?::uuid
		UNION ALL
		SELECT member_id::text AS member_id, 'manager' AS role, created_at FROM `+participantCharacterizationManagerTable+` WHERE `+participantCharacterizationEntityIDField+` = ?::uuid
	`, entityID, entityID).Scan(&rows).Error)
	result := make(map[string]participantCharacterizationRow, len(rows))
	for _, row := range rows {
		result[env.keyForMember(row.MemberID)+"/"+row.Role] = row
	}
	return result
}

func (env *participantCharacterizationEnv) roles(t *testing.T, entityID string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, row := range env.rows(t, entityID) {
		result[env.keyForMember(row.MemberID)] = row.Role
	}
	return result
}

func (env *participantCharacterizationEnv) keyForMember(memberID string) string {
	for key, actor := range env.actors {
		if actor.memberID == memberID {
			return key
		}
	}
	return memberID
}

func (env *participantCharacterizationEnv) keyForIdentity(identityID string) string {
	for key, actor := range env.actors {
		if actor.identityID == identityID {
			return key
		}
	}
	return identityID
}

func (env *participantCharacterizationEnv) relationships(t *testing.T, entityID string) []string {
	t.Helper()
	snapshots, _, err := env.spiceDB.SnapshotResourceRelationshipDescriptors(
		t.Context(), participantCharacterizationSnapshotPlan(t, entityID),
	)
	require.NoError(t, err)
	result := make([]string, 0, len(snapshots))
	for _, snapshot := range snapshots {
		require.Equal(t, participantCharacterizationEntity, snapshot.ResourceType())
		require.Equal(t, entityID, snapshot.ResourceID())
		subject := snapshot.SubjectType() + ":" + snapshot.SubjectID()
		if snapshot.SubjectType() == "account_identity" {
			subject = env.keyForIdentity(snapshot.SubjectID())
		}
		if snapshot.SubjectRelation() != "" {
			subject += "#" + snapshot.SubjectRelation()
		}
		result = append(result, snapshot.Relation()+"@"+subject)
	}
	sort.Strings(result)
	return result
}

func (env *participantCharacterizationEnv) audits(t *testing.T, entityID string) []participantCharacterizationAudit {
	t.Helper()
	var rows []struct {
		Action               string `gorm:"column:action"`
		TargetType           string `gorm:"column:target_type"`
		ActorMemberID        string `gorm:"column:actor_member_id"`
		SubjectMemberID      string `gorm:"column:subject_member_id"`
		PreviousRelationship string `gorm:"column:previous_relationship"`
		NewRelationship      string `gorm:"column:new_relationship"`
		ChangedFields        string `gorm:"column:changed_fields"`
	}
	require.NoError(t, env.db.Raw(`
		SELECT action, target_type, COALESCE(actor_member_id::text, '') AS actor_member_id,
		       COALESCE(attributes->>'subject_member_id', '') AS subject_member_id,
		       COALESCE(attributes->>'previous_relationship', '') AS previous_relationship,
		       COALESCE(attributes->>'new_relationship', '') AS new_relationship,
		       COALESCE(attributes->>'changed_fields', '') AS changed_fields
		FROM public.domain_audit
		WHERE target_id = ?
		ORDER BY occurred_at, audit_id
	`, entityID).Scan(&rows).Error)
	result := make([]participantCharacterizationAudit, 0, len(rows))
	for _, row := range rows {
		require.Equal(t, string(participantCharacterizationAuditAction), row.Action)
		require.Equal(t, participantCharacterizationEntity, row.TargetType)
		result = append(result, participantCharacterizationAudit{
			ActorMemberID: env.keyForMember(row.ActorMemberID), SubjectMemberID: env.keyForMember(row.SubjectMemberID),
			PreviousRelationship: row.PreviousRelationship, NewRelationship: row.NewRelationship,
			ChangedFields: row.ChangedFields,
		})
	}
	return result
}

func (env *participantCharacterizationEnv) entityUpdatedAt(t *testing.T, entityID string) time.Time {
	t.Helper()
	var updatedAt time.Time
	require.NoError(t, env.db.Table(participantCharacterizationEntity).Select("updated_at").
		Where("id = ?::uuid", entityID).Scan(&updatedAt).Error)
	return updatedAt
}

func (env *participantCharacterizationEnv) documentRevision(t *testing.T, entityID string) string {
	t.Helper()
	var revision string
	require.NoError(t, env.db.Raw(`
		SELECT document.revision::text
		FROM `+participantCharacterizationEntity+` AS entity
		JOIN content_document AS document ON document.id = entity.content_document_id
		WHERE entity.id = ?::uuid
	`, entityID).Scan(&revision).Error)
	return revision
}

func requireParticipantCharacterizationError(t *testing.T, err error, code connect.Code, message string) {
	t.Helper()
	require.Error(t, err)
	var connectErr *connect.Error
	require.True(t, errors.As(err, &connectErr), "error must be a Connect error: %v", err)
	require.Equal(t, code, connectErr.Code(), connectErr.Message())
	require.Equal(t, message, connectErr.Message())
}

type participantCharacterizationCase struct {
	name    string
	initial map[string]string
	caller  string
	// remove selects RemoveParticipant; otherwise SetParticipant runs with role.
	remove bool
	target string
	role   string
	// missingEntity points the request at an entity that does not exist.
	missingEntity bool

	wantCode    connect.Code
	wantMessage func(entityID, targetMemberID string) string
	wantRoles   map[string]string
	wantAudits  []participantCharacterizationAudit
}

func participantCharacterizationCases() []participantCharacterizationCase {
	entityNotFound := func(entityID, _ string) string {
		return participantCharacterizationEntity + " not found: " + entityID
	}
	fixed := func(message string) func(string, string) string {
		return func(string, string) string { return message }
	}
	lastOwner := fixed(participantCharacterizationArticle + " must retain at least one durable Owner")
	sameRole := fixed(participantCharacterizationTitle + " participant already has this role")
	audit := func(actor, previous, next string) participantCharacterizationAudit {
		return participantCharacterizationAudit{
			ActorMemberID: actor, SubjectMemberID: "target",
			PreviousRelationship: previous, NewRelationship: next, ChangedFields: `["participants"]`,
		}
	}
	return []participantCharacterizationCase{
		{
			name: "admin adds manager", initial: map[string]string{"ownerA": "owner"},
			caller: "admin", target: "target", role: participantRoleManager,
			wantRoles:  map[string]string{"ownerA": "owner", "target": "manager"},
			wantAudits: []participantCharacterizationAudit{audit("admin", "none", "manager")},
		},
		{
			name: "admin adds owner", initial: map[string]string{"ownerA": "owner"},
			caller: "admin", target: "target", role: participantRoleOwner,
			wantRoles:  map[string]string{"ownerA": "owner", "target": "owner"},
			wantAudits: []participantCharacterizationAudit{audit("admin", "none", "owner")},
		},
		{
			name: "admin promotes manager to owner", initial: map[string]string{"ownerA": "owner", "target": "manager"},
			caller: "admin", target: "target", role: participantRoleOwner,
			wantRoles:  map[string]string{"ownerA": "owner", "target": "owner"},
			wantAudits: []participantCharacterizationAudit{audit("admin", "manager", "owner")},
		},
		{
			name: "admin demotes one of two owners", initial: map[string]string{"ownerA": "owner", "target": "owner"},
			caller: "admin", target: "target", role: participantRoleManager,
			wantRoles:  map[string]string{"ownerA": "owner", "target": "manager"},
			wantAudits: []participantCharacterizationAudit{audit("admin", "owner", "manager")},
		},
		{
			name: "demoting the last owner is rejected", initial: map[string]string{"target": "owner"},
			caller: "admin", target: "target", role: participantRoleManager,
			wantCode: connect.CodeFailedPrecondition, wantMessage: lastOwner,
			wantRoles: map[string]string{"target": "owner"},
		},
		{
			name: "setting the current manager role is rejected", initial: map[string]string{"ownerA": "owner", "target": "manager"},
			caller: "admin", target: "target", role: participantRoleManager,
			wantCode: connect.CodeFailedPrecondition, wantMessage: sameRole,
			wantRoles: map[string]string{"ownerA": "owner", "target": "manager"},
		},
		{
			name: "setting the current owner role is rejected", initial: map[string]string{"ownerA": "owner", "target": "owner"},
			caller: "admin", target: "target", role: participantRoleOwner,
			wantCode: connect.CodeFailedPrecondition, wantMessage: sameRole,
			wantRoles: map[string]string{"ownerA": "owner", "target": "owner"},
		},
		{
			name: "admin removes manager", initial: map[string]string{"ownerA": "owner", "target": "manager"},
			caller: "admin", remove: true, target: "target",
			wantRoles:  map[string]string{"ownerA": "owner"},
			wantAudits: []participantCharacterizationAudit{audit("admin", "manager", "none")},
		},
		{
			name: "admin removes one of two owners", initial: map[string]string{"ownerA": "owner", "target": "owner"},
			caller: "admin", remove: true, target: "target",
			wantRoles:  map[string]string{"ownerA": "owner"},
			wantAudits: []participantCharacterizationAudit{audit("admin", "owner", "none")},
		},
		{
			name: "removing the last owner is rejected", initial: map[string]string{"target": "owner"},
			caller: "admin", remove: true, target: "target",
			wantCode: connect.CodeFailedPrecondition, wantMessage: lastOwner,
			wantRoles: map[string]string{"target": "owner"},
		},
		{
			name: "removing a non participant is not found", initial: map[string]string{"ownerA": "owner"},
			caller: "admin", remove: true, target: "target",
			wantCode: connect.CodeNotFound, wantMessage: fixed(participantCharacterizationTitle + " participant not found"),
			wantRoles: map[string]string{"ownerA": "owner"},
		},
		{
			name: "owner adds manager", initial: map[string]string{"ownerA": "owner"},
			caller: "ownerA", target: "target", role: participantRoleManager,
			wantRoles:  map[string]string{"ownerA": "owner", "target": "manager"},
			wantAudits: []participantCharacterizationAudit{audit("ownerA", "none", "manager")},
		},
		{
			name: "owner adds owner", initial: map[string]string{"ownerA": "owner"},
			caller: "ownerA", target: "target", role: participantRoleOwner,
			wantRoles:  map[string]string{"ownerA": "owner", "target": "owner"},
			wantAudits: []participantCharacterizationAudit{audit("ownerA", "none", "owner")},
		},
		{
			name: "owner removes manager", initial: map[string]string{"ownerA": "owner", "target": "manager"},
			caller: "ownerA", remove: true, target: "target",
			wantRoles:  map[string]string{"ownerA": "owner"},
			wantAudits: []participantCharacterizationAudit{audit("ownerA", "manager", "none")},
		},
		{
			name: "owner cannot demote another owner", initial: map[string]string{"ownerA": "owner", "target": "owner"},
			caller: "ownerA", target: "target", role: participantRoleManager,
			wantCode: connect.CodeNotFound, wantMessage: entityNotFound,
			wantRoles: map[string]string{"ownerA": "owner", "target": "owner"},
		},
		{
			name: "owner cannot remove another owner", initial: map[string]string{"ownerA": "owner", "target": "owner"},
			caller: "ownerA", remove: true, target: "target",
			wantCode: connect.CodeNotFound, wantMessage: entityNotFound,
			wantRoles: map[string]string{"ownerA": "owner", "target": "owner"},
		},
		{
			name: "owner sees the same role rejection for an owner", initial: map[string]string{"ownerA": "owner", "target": "owner"},
			caller: "ownerA", target: "target", role: participantRoleOwner,
			wantCode: connect.CodeFailedPrecondition, wantMessage: sameRole,
			wantRoles: map[string]string{"ownerA": "owner", "target": "owner"},
		},
		{
			name: "manager cannot add participants", initial: map[string]string{"ownerA": "owner", "outsider": "manager"},
			caller: "outsider", target: "target", role: participantRoleManager,
			wantCode: connect.CodeNotFound, wantMessage: entityNotFound,
			wantRoles: map[string]string{"ownerA": "owner", "outsider": "manager"},
		},
		{
			name: "manager cannot remove managers", initial: map[string]string{"ownerA": "owner", "outsider": "manager", "target": "manager"},
			caller: "outsider", remove: true, target: "target",
			wantCode: connect.CodeNotFound, wantMessage: entityNotFound,
			wantRoles: map[string]string{"ownerA": "owner", "outsider": "manager", "target": "manager"},
		},
		{
			name: "outsider cannot add participants", initial: map[string]string{"ownerA": "owner"},
			caller: "outsider", target: "target", role: participantRoleManager,
			wantCode: connect.CodeNotFound, wantMessage: entityNotFound,
			wantRoles: map[string]string{"ownerA": "owner"},
		},
		{
			name: "anonymous caller is not found", initial: map[string]string{"ownerA": "owner"},
			caller: "anonymous", target: "target", role: participantRoleManager,
			wantCode: connect.CodeNotFound, wantMessage: entityNotFound,
			wantRoles: map[string]string{"ownerA": "owner"},
		},
		{
			name: "unknown member is not found", initial: map[string]string{"ownerA": "owner"},
			caller: "admin", target: "unknown", role: participantRoleManager,
			wantCode: connect.CodeNotFound,
			wantMessage: func(_, memberID string) string {
				return "member not found: " + memberID
			},
			wantRoles: map[string]string{"ownerA": "owner"},
		},
		{
			name: "unspecified role is invalid", initial: map[string]string{"ownerA": "owner"},
			caller: "admin", target: "target", role: "",
			wantCode: connect.CodeInvalidArgument, wantMessage: fixed("role: must be Owner or Manager"),
			wantRoles: map[string]string{"ownerA": "owner"},
		},
		{
			name: "set on a missing entity is not found", missingEntity: true,
			caller: "admin", target: "target", role: participantRoleManager,
			wantCode: connect.CodeNotFound, wantMessage: entityNotFound,
		},
		{
			name: "remove on a missing entity is not found", missingEntity: true,
			caller: "admin", remove: true, target: "target",
			wantCode: connect.CodeNotFound, wantMessage: entityNotFound,
		},
	}
}

func participantCharacterizationExpectedRelationships(roles map[string]string) []string {
	result := []string{"policy@platform:global"}
	for key, role := range roles {
		result = append(result, role+"@"+key)
	}
	sort.Strings(result)
	return result
}

func TestParticipantCharacterizationIntegration(t *testing.T) {
	t.Parallel()
	env := newParticipantCharacterizationEnv(t)
	for _, tc := range participantCharacterizationCases() {
		t.Run(tc.name, func(t *testing.T) {
			entityID := uuid.NewString()
			if !tc.missingEntity {
				entityID = env.seedEntity(t, tc.initial)
			}
			targetMemberID := uuid.NewString()
			if actor, ok := env.actors[tc.target]; ok {
				targetMemberID = actor.memberID
			}
			var rowsBefore map[string]participantCharacterizationRow
			var updatedAtBefore time.Time
			var revisionBefore string
			var relationshipsBefore []string
			if !tc.missingEntity {
				rowsBefore = env.rows(t, entityID)
				updatedAtBefore = env.entityUpdatedAt(t, entityID)
				revisionBefore = env.documentRevision(t, entityID)
				relationshipsBefore = env.relationships(t, entityID)
				require.Equal(t, participantCharacterizationExpectedRelationships(tc.initial), relationshipsBefore)
			}
			signalsBefore := len(env.publisher.recorded())

			ctx := env.context(t, tc.caller)
			var setResponse *participantCharacterizationResponse
			var removed bool
			var err error
			if tc.remove {
				removed, err = env.service.remove(ctx, entityID, targetMemberID)
			} else {
				setResponse, err = env.service.set(ctx, entityID, targetMemberID, tc.role)
			}

			if tc.wantMessage != nil {
				requireParticipantCharacterizationError(t, err, tc.wantCode, tc.wantMessage(entityID, targetMemberID))
				require.Nil(t, setResponse)
				require.False(t, removed)
			} else {
				require.NoError(t, err)
				if tc.remove {
					require.True(t, removed)
				} else {
					require.NotNil(t, setResponse)
					target := env.actors[tc.target]
					require.Equal(t, target.memberID, setResponse.MemberID)
					require.Equal(t, target.nickname, setResponse.Nickname)
					require.Equal(t, tc.role, setResponse.Role)
					require.True(t, setResponse.HasEffectiveAuthority)
					stored := env.rows(t, entityID)["target/"+tc.role]
					require.Equal(t, stored.CreatedAt.UTC(), setResponse.CreatedAt.UTC(),
						"the response carries the created_at of the newly inserted relation row")
					if previous, existed := rowsBefore["target/"+tc.initial["target"]]; existed {
						require.True(t, setResponse.CreatedAt.After(previous.CreatedAt), "a role change inserts a new relation row")
					}
				}
			}
			if tc.missingEntity {
				return
			}

			require.Equal(t, tc.wantRoles, env.roles(t, entityID))
			for key, row := range env.rows(t, entityID) {
				if previous, ok := rowsBefore[key]; ok {
					require.Equal(t, previous.CreatedAt, row.CreatedAt, "untouched relation rows keep created_at")
				}
			}
			wantAudits := tc.wantAudits
			if wantAudits == nil {
				wantAudits = []participantCharacterizationAudit{}
			}
			require.Equal(t, wantAudits, env.audits(t, entityID))
			require.Equal(t, participantCharacterizationExpectedRelationships(tc.wantRoles), env.relationships(t, entityID))
			require.Equal(t, updatedAtBefore, env.entityUpdatedAt(t, entityID), "participant changes do not touch the entity row")
			require.Equal(t, revisionBefore, env.documentRevision(t, entityID), "participant changes do not advance the document revision")
			require.Len(t, env.publisher.recorded(), signalsBefore, "participant changes publish no events")
		})
	}
}
