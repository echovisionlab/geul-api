//go:build integration

package label

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// The document metadata characterization tests pin UpdateLabelDocumentMetadata.
// The shared cases run unchanged against UpdateArtistDocumentMetadata, which
// additionally carries real_name, label_ids and a source-locale gate.

const (
	documentMetadataCharacterizationParentColumn = "parent_label_id"
	// Artist echoes the normalized source locale; Label has no locale field.
	documentMetadataCharacterizationResponseLocale = ""
	documentMetadataCharacterizationCycleMessage   = "would create a label hierarchy cycle"
	documentMetadataCharacterizationStaleMessage   = "Label document changed since it was loaded; reload before saving"
)

type documentMetadataCharacterizationService struct {
	service *InternalConnectHandler
}

func newDocumentMetadataCharacterizationService(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	store *contentblock.Store,
	publisher *participantCharacterizationPublisher,
) documentMetadataCharacterizationService {
	t.Helper()
	return documentMetadataCharacterizationService{service: NewInternalConnectHandler(newTestService(t, Deps{
		DB: db, SpiceDB: spiceDB, Audit: apitelemetry.NewDurableWriter(db), Publisher: publisher, ContentBlocks: store,
	}))}
}

func saveDocumentMetadataCharacterizationTitle(t *testing.T, db *gorm.DB, entityID, title string, now time.Time) {
	t.Helper()
	require.NoError(t, saveLabelSourceLocaleDocumentState(t.Context(), db, entityID, "en", translationLocaleDocumentSaveInput{
		Title: &title, OverwriteNullFields: true, Now: now,
	}))
}

func documentMetadataCharacterizationParentMutation(t *testing.T, entityID, parentID string) policyv1.RelationshipMutation {
	t.Helper()
	mutation, err := policyv1.Label.TouchParent(entityID, parentID)
	require.NoError(t, err)
	return mutation
}

func (s documentMetadataCharacterizationService) update(
	ctx context.Context,
	entityID string,
	expectedRevision string,
	contributors []string,
	spec *documentMetadataCharacterizationUpdate,
) (*documentMetadataCharacterizationResponse, error) {
	var update *intrav1.LabelDocumentMetadataUpdate
	if spec != nil {
		update = &intrav1.LabelDocumentMetadataUpdate{
			Slug: spec.Slug, CountryCode: spec.CountryCode, Website: spec.Website, ParentLabelId: spec.Parent,
		}
		if spec.SocialLinks != nil {
			update.SocialLinks = &intrav1.LabelSocialLinksValue{Values: spec.SocialLinks}
		}
	}
	response, err := s.service.UpdateLabelDocumentMetadata(ctx, connect.NewRequest(&intrav1.UpdateLabelDocumentMetadataRequest{
		LabelId: entityID, ExpectedRevision: expectedRevision, ContributorMemberIds: contributors, Update: update,
	}))
	if err != nil {
		return nil, err
	}
	return &documentMetadataCharacterizationResponse{
		DocumentRevision: response.Msg.GetDocumentRevision(), Changed: response.Msg.GetChanged(),
		SourceChanged: response.Msg.GetSourceChanged(),
	}, nil
}

// ---- Scenario body: keep identical to the Artist document metadata characterization test. ----

type documentMetadataCharacterizationUpdate struct {
	Slug        *intrav1.NullableStringMutation
	CountryCode *intrav1.NullableStringMutation
	Website     *intrav1.NullableStringMutation
	Parent      *intrav1.NullableStringMutation
	SocialLinks map[string]string
}

type documentMetadataCharacterizationResponse struct {
	DocumentRevision string
	Changed          bool
	SourceChanged    bool
	Locale           string
}

type documentMetadataCharacterizationRow struct {
	Slug        *string   `gorm:"column:slug"`
	CountryCode *string   `gorm:"column:country_code"`
	Website     *string   `gorm:"column:website"`
	ParentID    *string   `gorm:"column:parent_id"`
	SocialLinks string    `gorm:"column:social_links"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

type documentMetadataCharacterizationTranslation struct {
	Locale    string    `gorm:"column:locale"`
	Title     *string   `gorm:"column:title"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

type documentMetadataCharacterizationEnv struct {
	db           *gorm.DB
	spiceDB      *auth.SpiceDBClient
	store        *contentblock.Store
	service      documentMetadataCharacterizationService
	publisher    *participantCharacterizationPublisher
	ctx          context.Context
	contributor  string
	unauthorized string
}

func newDocumentMetadataCharacterizationEnv(t *testing.T) *documentMetadataCharacterizationEnv {
	t.Helper()
	db := testutil.NewIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	store := testutil.NewCreativeContentStore(t, stack.SpiceDBClient)
	publisher := &participantCharacterizationPublisher{}
	adminIdentityID := testutil.IntegrationUUID()
	adminMemberID := testutil.SeedPostIntegrationIdentity(t, db, adminIdentityID, "Metadata admin")
	testutil.GrantIntegrationGlobalRole(t, stack.SpiceDBClient, adminIdentityID, policyv1.Role.Admin())
	outsiderIdentityID := testutil.IntegrationUUID()
	outsiderMemberID := testutil.SeedPostIntegrationIdentity(t, db, outsiderIdentityID, "Metadata outsider")
	return &documentMetadataCharacterizationEnv{
		db: db, spiceDB: stack.SpiceDBClient, store: store,
		service:   newDocumentMetadataCharacterizationService(t, db, stack.SpiceDBClient, store, publisher),
		publisher: publisher, ctx: testutil.NewAuditContext(t, adminIdentityID, adminMemberID),
		contributor: adminMemberID, unauthorized: outsiderMemberID,
	}
}

func (env *documentMetadataCharacterizationEnv) seedEntity(t *testing.T, parentID *string) string {
	t.Helper()
	entityID := seedParticipantCharacterizationEntity(t, env.db, env.store)
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	require.NoError(t, env.db.Exec(`
		UPDATE `+participantCharacterizationEntity+`
		SET slug = ?, country_code = 'KR', website = 'https://initial.example',
		    social_links = '{"instagram":"initial"}'::jsonb, `+documentMetadataCharacterizationParentColumn+` = ?,
		    updated_at = ?
		WHERE id = ?::uuid
	`, "initial-"+entityID, parentID, now, entityID).Error)
	saveDocumentMetadataCharacterizationTitle(t, env.db, entityID, "Initial title", now)
	mutations := []policyv1.RelationshipMutation{participantCharacterizationPolicyMutation(t, entityID)}
	if parentID != nil {
		mutations = append(mutations, documentMetadataCharacterizationParentMutation(t, entityID, *parentID))
	}
	_, err := env.spiceDB.ApplyRelationships(t.Context(), mutations...)
	require.NoError(t, err)
	return entityID
}

func (env *documentMetadataCharacterizationEnv) row(t *testing.T, entityID string) documentMetadataCharacterizationRow {
	t.Helper()
	var row documentMetadataCharacterizationRow
	require.NoError(t, env.db.Raw(`
		SELECT slug, country_code, website, `+documentMetadataCharacterizationParentColumn+`::text AS parent_id,
		       social_links::text AS social_links, updated_at
		FROM `+participantCharacterizationEntity+` WHERE id = ?::uuid
	`, entityID).Scan(&row).Error)
	return row
}

func (env *documentMetadataCharacterizationEnv) translations(t *testing.T, entityID string) []documentMetadataCharacterizationTranslation {
	t.Helper()
	var rows []documentMetadataCharacterizationTranslation
	require.NoError(t, env.db.Raw(`
		SELECT locale, title, updated_at FROM `+participantCharacterizationEntity+`_translation
		WHERE entity_id = ?::uuid ORDER BY locale
	`, entityID).Scan(&rows).Error)
	return rows
}

func (env *documentMetadataCharacterizationEnv) revision(t *testing.T, entityID string) string {
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

// relationships renders the snapshot of refs.entityID with entity IDs replaced
// by their role in the case; the snapshot also returns child tuples that name
// the entity as their parent.
func (env *documentMetadataCharacterizationEnv) relationships(t *testing.T, refs documentMetadataCharacterizationRefs) []string {
	t.Helper()
	snapshots, _, err := env.spiceDB.SnapshotResourceRelationshipDescriptors(
		t.Context(), participantCharacterizationSnapshotPlan(t, refs.entityID),
	)
	require.NoError(t, err)
	name := func(objectType, id string) string {
		if objectType != participantCharacterizationEntity {
			return objectType + ":" + id
		}
		switch id {
		case refs.entityID:
			return "entity"
		case refs.parentID:
			return "parent"
		case refs.childID:
			return "child"
		}
		return objectType + ":" + id
	}
	result := make([]string, 0, len(snapshots))
	for _, snapshot := range snapshots {
		result = append(result, name(snapshot.ResourceType(), snapshot.ResourceID())+"#"+snapshot.Relation()+"@"+
			name(snapshot.SubjectType(), snapshot.SubjectID()))
	}
	sort.Strings(result)
	return result
}

func (env *documentMetadataCharacterizationEnv) auditCount(t *testing.T, entityID string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, env.db.Raw(`SELECT count(*) FROM public.domain_audit WHERE target_id = ?`, entityID).Scan(&count).Error)
	return count
}

func documentMetadataSet(value string) *intrav1.NullableStringMutation {
	return &intrav1.NullableStringMutation{Operation: &intrav1.NullableStringMutation_Set{Set: value}}
}

func documentMetadataClear() *intrav1.NullableStringMutation {
	return &intrav1.NullableStringMutation{Operation: &intrav1.NullableStringMutation_Clear{Clear: true}}
}

func documentMetadataCharacterizationString(value string) *string { return &value }

func requireDocumentMetadataCharacterizationError(t *testing.T, err error, code connect.Code, message string) {
	t.Helper()
	require.Error(t, err)
	var connectErr *connect.Error
	require.True(t, errors.As(err, &connectErr), "error must be a Connect error: %v", err)
	require.Equal(t, code, connectErr.Code(), connectErr.Message())
	require.Equal(t, message, connectErr.Message())
}

// documentMetadataCharacterizationRefs names the entities a case refers to.
type documentMetadataCharacterizationRefs struct {
	entityID  string
	parentID  string
	childID   string
	takenSlug string
}

type documentMetadataCharacterizationCase struct {
	name string
	// initialParent seeds the entity under refs.parentID.
	initialParent bool
	update        func(refs documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate
	// revision overrides the expected revision; nil sends the current one.
	revision      func(current string) string
	contributors  func(env *documentMetadataCharacterizationEnv) []string
	missingEntity bool

	wantCode    connect.Code
	wantMessage func(refs documentMetadataCharacterizationRefs) string
	wantChanged bool
	// wantRow applies the expected column changes to the seeded row.
	wantRow func(refs documentMetadataCharacterizationRefs, row *documentMetadataCharacterizationRow)
}

func documentMetadataCharacterizationCommonCases() []documentMetadataCharacterizationCase {
	fixed := func(message string) func(documentMetadataCharacterizationRefs) string {
		return func(documentMetadataCharacterizationRefs) string { return message }
	}
	parentField := documentMetadataCharacterizationParentColumn
	entity := participantCharacterizationEntity
	title := participantCharacterizationTitle
	return []documentMetadataCharacterizationCase{
		{
			name: "sets every shared field",
			update: func(documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{
					Slug: documentMetadataSet("  next-slug-value  "), CountryCode: documentMetadataSet("US"),
					Website: documentMetadataSet("https://next.example"), SocialLinks: map[string]string{"x": "next"},
				}
			},
			wantChanged: true,
			wantRow: func(_ documentMetadataCharacterizationRefs, row *documentMetadataCharacterizationRow) {
				row.Slug = documentMetadataCharacterizationString("next-slug-value")
				row.CountryCode = documentMetadataCharacterizationString("US")
				row.Website = documentMetadataCharacterizationString("https://next.example")
				row.SocialLinks = `{"x": "next"}`
			},
		},
		{
			name: "clears nullable fields",
			update: func(documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{
					Slug: documentMetadataClear(), CountryCode: documentMetadataClear(), Website: documentMetadataClear(),
				}
			},
			wantChanged: true,
			wantRow: func(_ documentMetadataCharacterizationRefs, row *documentMetadataCharacterizationRow) {
				row.Slug, row.CountryCode, row.Website = nil, nil, nil
			},
		},
		{
			name: "empty social links replace the map",
			update: func(documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{SocialLinks: map[string]string{}}
			},
			wantChanged: true,
			wantRow: func(_ documentMetadataCharacterizationRefs, row *documentMetadataCharacterizationRow) {
				row.SocialLinks = `{}`
			},
		},
		{
			name: "current values are a no-op",
			update: func(refs documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{
					Slug: documentMetadataSet(" initial-" + refs.entityID + " "), CountryCode: documentMetadataSet("KR"),
					Website: documentMetadataSet("https://initial.example"), SocialLinks: map[string]string{"instagram": "initial"},
				}
			},
		},
		{
			name: "sets the parent",
			update: func(refs documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{Parent: documentMetadataSet(refs.parentID)}
			},
			wantChanged: true,
			wantRow: func(refs documentMetadataCharacterizationRefs, row *documentMetadataCharacterizationRow) {
				row.ParentID = documentMetadataCharacterizationString(refs.parentID)
			},
		},
		{
			name: "clears the parent", initialParent: true,
			update: func(documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{Parent: documentMetadataClear()}
			},
			wantChanged: true,
			wantRow: func(_ documentMetadataCharacterizationRefs, row *documentMetadataCharacterizationRow) {
				row.ParentID = nil
			},
		},
		{
			name: "the current parent is a no-op", initialParent: true,
			update: func(refs documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{Parent: documentMetadataSet(refs.parentID)}
			},
		},
		{
			name: "self parent is rejected",
			update: func(refs documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{Parent: documentMetadataSet(refs.entityID)}
			},
			wantCode: connect.CodeInvalidArgument, wantMessage: fixed(parentField + ": cannot reference the same " + entity),
		},
		{
			name: "missing parent is rejected",
			update: func(documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{Parent: documentMetadataSet(uuid.NewString())}
			},
			wantCode: connect.CodeInvalidArgument, wantMessage: fixed(parentField + ": " + entity + " does not exist"),
		},
		{
			name: "descendant parent is rejected",
			update: func(refs documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{Parent: documentMetadataSet(refs.childID)}
			},
			wantCode: connect.CodeInvalidArgument, wantMessage: fixed(parentField + ": " + documentMetadataCharacterizationCycleMessage),
		},
		{
			name: "invalid parent ID is rejected",
			update: func(documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{Parent: documentMetadataSet("not-a-uuid")}
			},
			wantCode: connect.CodeInvalidArgument, wantMessage: fixed(parentField + ": must be a valid " + title + " ID"),
		},
		{
			name: "taken slug is rejected",
			update: func(refs documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{Slug: documentMetadataSet(refs.takenSlug)}
			},
			wantCode: connect.CodeAlreadyExists,
			wantMessage: func(refs documentMetadataCharacterizationRefs) string {
				return entity + " with slug '" + refs.takenSlug + "' already exists"
			},
		},
		{
			name: "slug with a slash is rejected",
			update: func(documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{Slug: documentMetadataSet("a/b")}
			},
			wantCode: connect.CodeInvalidArgument, wantMessage: fixed("slug: must not contain '/'"),
		},
		{
			name:     "nil update is rejected",
			update:   func(documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate { return nil },
			wantCode: connect.CodeInvalidArgument, wantMessage: fixed("update is required"),
		},
		{
			name: "empty update is rejected",
			update: func(documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{}
			},
			wantCode: connect.CodeInvalidArgument, wantMessage: fixed("update: at least one metadata value is required"),
		},
		{
			name: "blank set value is rejected",
			update: func(documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{Website: documentMetadataSet("   ")}
			},
			wantCode: connect.CodeInvalidArgument, wantMessage: fixed("website: set value must not be empty; use clear instead"),
		},
		{
			name: "clear false is rejected",
			update: func(documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{CountryCode: &intrav1.NullableStringMutation{
					Operation: &intrav1.NullableStringMutation_Clear{Clear: false},
				}}
			},
			wantCode: connect.CodeInvalidArgument, wantMessage: fixed("country_code: clear must be true"),
		},
		{
			name: "mutation without operation is rejected",
			update: func(documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{Slug: &intrav1.NullableStringMutation{}}
			},
			wantCode: connect.CodeInvalidArgument, wantMessage: fixed("slug: set or clear is required"),
		},
		{
			name: "malformed expected revision is rejected",
			update: func(documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{CountryCode: documentMetadataSet("US")}
			},
			revision: func(string) string { return "not-a-revision" },
			wantCode: connect.CodeInvalidArgument, wantMessage: fixed("expected_revision: must be a UUID"),
		},
		{
			name: "stale expected revision is rejected",
			update: func(documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{CountryCode: documentMetadataSet("US")}
			},
			revision: func(string) string { return uuid.NewString() },
			wantCode: connect.CodeFailedPrecondition, wantMessage: fixed(documentMetadataCharacterizationStaleMessage),
		},
		{
			name: "contributor without edit authority is rejected",
			update: func(documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{CountryCode: documentMetadataSet("US")}
			},
			contributors: func(env *documentMetadataCharacterizationEnv) []string { return []string{env.unauthorized} },
			wantCode:     connect.CodePermissionDenied, wantMessage: fixed("no permission to COLLABORATION_PERMISSION_EDIT this collaboration resource"),
		},
		{
			name: "missing entity is not found", missingEntity: true,
			update: func(documentMetadataCharacterizationRefs) *documentMetadataCharacterizationUpdate {
				return &documentMetadataCharacterizationUpdate{CountryCode: documentMetadataSet("US")}
			},
			wantCode: connect.CodeNotFound,
			wantMessage: func(refs documentMetadataCharacterizationRefs) string {
				return entity + " not found: " + refs.entityID
			},
		},
	}
}

func runDocumentMetadataCharacterizationCase(
	t *testing.T,
	env *documentMetadataCharacterizationEnv,
	tc documentMetadataCharacterizationCase,
	call func(ctx context.Context, entityID, revision string, contributors []string, update *documentMetadataCharacterizationUpdate) (*documentMetadataCharacterizationResponse, error),
) (documentMetadataCharacterizationRefs, *documentMetadataCharacterizationResponse) {
	t.Helper()
	refs := documentMetadataCharacterizationRefs{parentID: env.seedEntity(t, nil)}
	other := env.seedEntity(t, nil)
	refs.takenSlug = "initial-" + other
	var initialParent *string
	if tc.initialParent {
		initialParent = &refs.parentID
	}
	if tc.missingEntity {
		refs.entityID = uuid.NewString()
	} else {
		refs.entityID = env.seedEntity(t, initialParent)
		refs.childID = env.seedEntity(t, &refs.entityID)
	}

	var before documentMetadataCharacterizationRow
	var translationsBefore []documentMetadataCharacterizationTranslation
	var relationshipsBefore []string
	revisionBefore := uuid.NewString()
	if !tc.missingEntity {
		before = env.row(t, refs.entityID)
		translationsBefore = env.translations(t, refs.entityID)
		relationshipsBefore = env.relationships(t, refs)
		revisionBefore = env.revision(t, refs.entityID)
	}
	signalsBefore := len(env.publisher.recorded())
	revision := revisionBefore
	if tc.revision != nil {
		revision = tc.revision(revisionBefore)
	}
	contributors := []string{env.contributor}
	if tc.contributors != nil {
		contributors = tc.contributors(env)
	}

	response, err := call(env.ctx, refs.entityID, revision, contributors, tc.update(refs))
	require.Len(t, env.publisher.recorded(), signalsBefore, "document metadata updates publish no events")
	if tc.missingEntity {
		requireDocumentMetadataCharacterizationError(t, err, tc.wantCode, tc.wantMessage(refs))
		return refs, nil
	}
	require.Equal(t, translationsBefore, env.translations(t, refs.entityID), "document metadata never writes translation rows")
	require.Zero(t, env.auditCount(t, refs.entityID), "document metadata updates write no domain audit")
	if tc.wantMessage != nil {
		requireDocumentMetadataCharacterizationError(t, err, tc.wantCode, tc.wantMessage(refs))
		require.Nil(t, response)
		require.Equal(t, before, env.row(t, refs.entityID))
		require.Equal(t, revisionBefore, env.revision(t, refs.entityID))
		require.Equal(t, relationshipsBefore, env.relationships(t, refs))
		return refs, nil
	}

	require.NoError(t, err)
	require.Equal(t, tc.wantChanged, response.Changed)
	require.False(t, response.SourceChanged, "metadata never changes the translation source")
	require.Equal(t, env.revision(t, refs.entityID), response.DocumentRevision)
	after := env.row(t, refs.entityID)
	want := before
	if tc.wantRow != nil {
		tc.wantRow(refs, &want)
	}
	if tc.wantChanged {
		require.NotEqual(t, revisionBefore, response.DocumentRevision)
		require.True(t, after.UpdatedAt.After(before.UpdatedAt), "a column change advances updated_at")
		want.UpdatedAt = after.UpdatedAt
	} else {
		require.Equal(t, revisionBefore, response.DocumentRevision)
	}
	require.JSONEq(t, want.SocialLinks, after.SocialLinks)
	want.SocialLinks = after.SocialLinks
	require.Equal(t, want, after)

	wantRelationships := []string{"child#parent@entity", "entity#policy@platform:global"}
	if after.ParentID != nil {
		require.Equal(t, refs.parentID, *after.ParentID)
		wantRelationships = append(wantRelationships, "entity#parent@parent")
	}
	sort.Strings(wantRelationships)
	require.Equal(t, wantRelationships, env.relationships(t, refs))
	return refs, response
}

func TestDocumentMetadataCharacterizationIntegration(t *testing.T) {
	t.Parallel()
	env := newDocumentMetadataCharacterizationEnv(t)
	for _, tc := range documentMetadataCharacterizationCommonCases() {
		t.Run(tc.name, func(t *testing.T) {
			_, response := runDocumentMetadataCharacterizationCase(t, env, tc, env.service.update)
			if response != nil {
				require.Equal(t, documentMetadataCharacterizationResponseLocale, response.Locale)
			}
		})
	}
}
