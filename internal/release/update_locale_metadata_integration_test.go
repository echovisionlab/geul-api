//go:build integration

package release_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/releasetest"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// These tests pin UpdateReleaseLocaleMetadata as it behaves before its
// decomposition: every rejection, both locale paths and the rows, revisions,
// audit records and side effects each one leaves behind.

const (
	releaseMetadataSourceTitle = "Source title"
	releaseMetadataTargetTitle = "Titre"
)

type recordingReleaseContentOG struct{ reasons []string }

func (og *recordingReleaseContentOG) RequestCurrent(_ context.Context, _ *gorm.DB, _ string, reason string) error {
	og.reasons = append(og.reasons, reason)
	return nil
}

// recordingReleaseAsyncPublisher counts every publish so a test can pin that an
// operation emits no content event or durable command.
type recordingReleaseAsyncPublisher struct{ calls int }

func (p *recordingReleaseAsyncPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	p.calls++
	return nil
}

func (p *recordingReleaseAsyncPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	p.calls++
	return nil
}

func (p *recordingReleaseAsyncPublisher) EnqueueProtobufWithExecutor(context.Context, eventpkg.DBTX, string, string, proto.Message) error {
	p.calls++
	return nil
}

func (p *recordingReleaseAsyncPublisher) NotifyProtobufWithExecutor(context.Context, eventpkg.DBTX, string, proto.Message) error {
	p.calls++
	return nil
}

type releaseLocaleMetadataFixture struct {
	db             *gorm.DB
	service        *releasepkg.InternalConnectHandler
	og             *recordingReleaseContentOG
	publisher      *recordingReleaseAsyncPublisher
	ctx            context.Context
	releaseID      string
	documentID     string
	contributor    string
	notedCredit    string
	plainCredit    string
	targetRevision string
}

// newReleaseLocaleMetadataFixture creates an English-source Release with two
// credits (only the first carries a source note) and an explicit French target
// locale.
func newReleaseLocaleMetadataFixture(t *testing.T) releaseLocaleMetadataFixture {
	t.Helper()
	db := testutil.NewIntegrationDB(t)
	spiceDB := integrationSpiceDB(t)
	adminID := testutil.IntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, adminID, "Release metadata admin")
	releases := newReleaseIntegrationService(t, db, adminID, &recordingReleaseDeleteFileDeleter{})
	created, err := releases.CreateRelease(releaseIntegrationAdminCtx(adminID), connect.NewRequest(&managev1.CreateReleaseRequest{
		Title:    releaseMetadataSourceTitle,
		Type:     managev1.ReleaseType_RELEASE_TYPE_ALBUM,
		Document: creativeContentIntegrationDocument("en", "Release metadata body"),
	}))
	require.NoError(t, err)
	releaseID := created.Msg.Id

	notedCredit, plainCredit := testutil.IntegrationUUID(), testutil.IntegrationUUID()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for index, creditID := range []string{notedCredit, plainCredit} {
		require.NoError(t, db.Exec(
			`INSERT INTO release_credit (id, release_id, credited_name, sort_order, created_at) VALUES (?::uuid, ?::uuid, ?, ?, ?)`,
			creditID, releaseID, "Credit "+creditID, index, now,
		).Error)
	}
	require.NoError(t, db.Exec(
		`INSERT INTO release_credit_locale (credit_id, locale, note, updated_at) VALUES (?::uuid, 'en', 'Source note', ?)`,
		notedCredit, now,
	).Error)

	store := newPageIntegrationContentBlockStore(t, spiceDB)
	targetRevision := releasepkg.SeedReleaseTargetLocaleForTest(
		t, db, store, releaseID, "fr", releaseMetadataTargetTitle, map[string]string{notedCredit: "Note francaise"},
	)
	og := &recordingReleaseContentOG{}
	publisher := &recordingReleaseAsyncPublisher{}
	service := releasepkg.NewInternalConnectHandler(releasetest.NewService(t, releasepkg.Deps{
		DB: db, SpiceDB: spiceDB, Audit: apitelemetry.NewDurableWriter(db), ContentBlocks: store,
		Checkpoints: testcollaboration.NewCheckpoints(db, spiceDB), ContentSignals: publisher, ContentOG: og,
	}))
	var documentID string
	require.NoError(t, db.Raw(`SELECT content_document_id::text FROM release WHERE id = ?`, releaseID).Scan(&documentID).Error)
	contributor := testutil.PostIntegrationMemberID(adminID)
	return releaseLocaleMetadataFixture{
		db: db, service: service, og: og, publisher: publisher,
		// editor-collab propagates the editing Member as the request actor.
		ctx:       testutil.NewAuditContext(t, adminID, contributor),
		releaseID: releaseID, documentID: documentID,
		contributor: contributor,
		notedCredit: notedCredit, plainCredit: plainCredit,
		targetRevision: targetRevision,
	}
}

func (f releaseLocaleMetadataFixture) request(t *testing.T, locale string) *intrav1.UpdateReleaseLocaleMetadataRequest {
	t.Helper()
	return &intrav1.UpdateReleaseLocaleMetadataRequest{
		ReleaseId: f.releaseID, Locale: locale,
		ExpectedRevision:     f.documentRevision(t),
		ContributorMemberIds: []string{f.contributor},
	}
}

func (f releaseLocaleMetadataFixture) documentRevision(t *testing.T) string {
	t.Helper()
	var revision string
	require.NoError(t, f.db.Raw(`SELECT revision::text FROM content_document WHERE id = ?::uuid`, f.documentID).Scan(&revision).Error)
	return revision
}

// releaseLocaleMetadataState is everything UpdateReleaseLocaleMetadata may
// write: locale titles, per-locale credit notes, the shared document revision,
// the Release timestamp and Domain Audit records.
type releaseLocaleMetadataState struct {
	Titles           map[string]*string
	Notes            map[string]map[string]string
	DocumentRevision string
	ReleaseUpdatedAt time.Time
	Audits           []releaseAuditRow
}

type releaseAuditRow struct {
	Action        string
	ChangedFields pq.StringArray `gorm:"type:text[]"`
	Locale        *string
	ItemOperation *string
	ActorKind     string
	ActorMemberID *string
}

func (f releaseLocaleMetadataFixture) state(t *testing.T) releaseLocaleMetadataState {
	t.Helper()
	state := releaseLocaleMetadataState{
		Titles:           map[string]*string{},
		Notes:            map[string]map[string]string{},
		DocumentRevision: f.documentRevision(t),
	}
	var titles []struct {
		Locale string
		Title  *string
	}
	require.NoError(t, f.db.Raw(`SELECT locale, title FROM release_translation WHERE entity_id = ?::uuid`, f.releaseID).Scan(&titles).Error)
	for _, row := range titles {
		state.Titles[row.Locale] = row.Title
	}
	var notes []struct {
		Locale   string
		CreditID string
		Note     string
	}
	require.NoError(t, f.db.Raw(`
		SELECT localized.locale, localized.credit_id::text AS credit_id, localized.note
		FROM release_credit_locale AS localized
		JOIN release_credit AS credit ON credit.id = localized.credit_id
		WHERE credit.release_id = ?::uuid`, f.releaseID).Scan(&notes).Error)
	for _, row := range notes {
		if state.Notes[row.Locale] == nil {
			state.Notes[row.Locale] = map[string]string{}
		}
		state.Notes[row.Locale][row.CreditID] = row.Note
	}
	require.NoError(t, f.db.Raw(`SELECT updated_at FROM release WHERE id = ?::uuid`, f.releaseID).Scan(&state.ReleaseUpdatedAt).Error)
	state.Audits = readReleaseAuditRows(t, f.db, f.releaseID)
	return state
}

func readReleaseAuditRows(t *testing.T, db *gorm.DB, releaseID string) []releaseAuditRow {
	t.Helper()
	var rows []releaseAuditRow
	require.NoError(t, db.Raw(`
		SELECT action,
		       ARRAY(SELECT jsonb_array_elements_text(COALESCE(attributes->'changed_fields', '[]'::jsonb))) AS changed_fields,
		       attributes->>'locale' AS locale,
		       attributes->>'item_operation' AS item_operation,
		       actor_kind, actor_member_id::text AS actor_member_id
		FROM public.domain_audit
		WHERE target_type = 'release' AND target_id = ?
		ORDER BY occurred_at, audit_id`, releaseID).Scan(&rows).Error)
	return rows
}

func requireConnectError(t *testing.T, err error, code connect.Code, message string) {
	t.Helper()
	require.Error(t, err)
	var connectErr *connect.Error
	require.True(t, errors.As(err, &connectErr), "error %v is not a Connect error", err)
	require.Equal(t, code, connectErr.Code(), "error: %v", err)
	require.Equal(t, message, connectErr.Message())
}

func releaseCreditNotes(values map[string]string) *intrav1.ReleaseCreditNotesValue {
	notes := &intrav1.ReleaseCreditNotesValue{}
	for creditID, note := range values {
		notes.Values = append(notes.Values, &intrav1.ReleaseCreditNote{CreditId: creditID, Note: note})
	}
	return notes
}

func TestUpdateReleaseLocaleMetadataRejectionsLeaveStateUnchangedIntegration(t *testing.T) {
	t.Parallel()
	fixture := newReleaseLocaleMetadataFixture(t)
	foreignCredit := testutil.IntegrationUUID()
	unknownRelease := testutil.IntegrationUUID()
	stale := "stale-target-revision"

	tests := []struct {
		name    string
		mutate  func(*intrav1.UpdateReleaseLocaleMetadataRequest)
		code    connect.Code
		message string
	}{
		{
			name:    "no metadata value",
			mutate:  func(*intrav1.UpdateReleaseLocaleMetadataRequest) {},
			code:    connect.CodeInvalidArgument,
			message: "update: at least one metadata value is required",
		},
		{
			name: "non-canonical locale",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.Locale, request.Title = "english", proto.String("Title")
			},
			code:    connect.CodeInvalidArgument,
			message: "locale: must be an exact canonical locale",
		},
		{
			name: "expected revision is not a UUID",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.ExpectedRevision, request.Title = "not-a-uuid", proto.String("Title")
			},
			code:    connect.CodeInvalidArgument,
			message: "expected_revision: must be a UUID",
		},
		{
			name: "expected revision is the nil UUID",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.ExpectedRevision, request.Title = uuid.Nil.String(), proto.String("Title")
			},
			code:    connect.CodeInvalidArgument,
			message: "expected_revision: must be a UUID",
		},
		{
			name: "unknown release",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.ReleaseId, request.Title = unknownRelease, proto.String("Title")
			},
			code:    connect.CodeNotFound,
			message: "release not found: " + unknownRelease,
		},
		{
			name: "no contributors",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.ContributorMemberIds, request.Title = nil, proto.String("Title")
			},
			code:    connect.CodeInvalidArgument,
			message: "contributor_member_ids: collaboration mutation requires contributors",
		},
		{
			name: "unknown contributor",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.ContributorMemberIds, request.Title = []string{testutil.IntegrationUUID()}, proto.String("Title")
			},
			code:    connect.CodeInvalidArgument,
			message: "contributor_member_ids: contains a Member that does not exist",
		},
		{
			name: "null credit note entry",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.CreditNotes = &intrav1.ReleaseCreditNotesValue{Values: []*intrav1.ReleaseCreditNote{nil}}
			},
			code:    connect.CodeInvalidArgument,
			message: "credit_notes: must not contain null entries",
		},
		{
			name: "blank credit id",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.CreditNotes = releaseCreditNotes(map[string]string{" ": "Note"})
			},
			code:    connect.CodeInvalidArgument,
			message: "credit_notes: credit_id is required",
		},
		{
			name: "empty source credit note",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.CreditNotes = releaseCreditNotes(map[string]string{fixture.notedCredit: "  "})
			},
			code:    connect.CodeInvalidArgument,
			message: "credit_notes: note must not be empty",
		},
		{
			name: "credit of another release",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.CreditNotes = releaseCreditNotes(map[string]string{foreignCredit: "Note"})
			},
			code:    connect.CodeInvalidArgument,
			message: "credit_notes: credit_id does not belong to the Release",
		},
		{
			name: "duplicate credit id",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.CreditNotes = &intrav1.ReleaseCreditNotesValue{Values: []*intrav1.ReleaseCreditNote{
					{CreditId: fixture.notedCredit, Note: "One"},
					{CreditId: " " + fixture.notedCredit, Note: "Two"},
				}}
			},
			code:    connect.CodeInvalidArgument,
			message: "credit_notes: credit_id must be unique",
		},
		{
			name: "source room carries a target revision",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.Title, request.ExpectedTargetRevision = proto.String("Title"), proto.String(fixture.targetRevision)
			},
			code:    connect.CodeInvalidArgument,
			message: "expected_target_revision: source Release room cannot carry a target revision",
		},
		{
			name: "blank source title",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.Title = proto.String("   ")
			},
			code:    connect.CodeInvalidArgument,
			message: "title: must not be empty",
		},
		{
			name: "stale source document revision",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.ExpectedRevision, request.Title = testutil.IntegrationUUID(), proto.String("Title")
			},
			code:    connect.CodeFailedPrecondition,
			message: "release content revision changed; reload before saving",
		},
		{
			name: "stale target document revision",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.Locale, request.ExpectedRevision = "fr", testutil.IntegrationUUID()
				request.Title, request.ExpectedTargetRevision = proto.String("Title"), proto.String(fixture.targetRevision)
			},
			code:    connect.CodeFailedPrecondition,
			message: "release content revision changed; reload before saving",
		},
		{
			name: "stale target revision",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.Locale, request.Title, request.ExpectedTargetRevision = "fr", proto.String("Title"), &stale
			},
			code:    connect.CodeFailedPrecondition,
			message: "translation target revision conflict",
		},
		{
			name: "existing target without target revision",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.Locale, request.Title = "fr", proto.String("Title")
			},
			code:    connect.CodeFailedPrecondition,
			message: "translation target revision conflict",
		},
		{
			name: "target locale not created yet",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.Locale, request.Title = "ko", proto.String("Title")
			},
			code:    connect.CodeFailedPrecondition,
			message: "Release target locale must be explicitly created before collaboration",
		},
		{
			name: "target note for credit of another release",
			mutate: func(request *intrav1.UpdateReleaseLocaleMetadataRequest) {
				request.Locale, request.ExpectedTargetRevision = "fr", proto.String(fixture.targetRevision)
				request.CreditNotes = releaseCreditNotes(map[string]string{foreignCredit: "Note"})
			},
			code:    connect.CodeInvalidArgument,
			message: "credit_notes: credit_id does not belong to the Release",
		},
	}

	before := fixture.state(t)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := fixture.request(t, "en")
			test.mutate(request)
			response, err := fixture.service.UpdateReleaseLocaleMetadata(fixture.ctx, connect.NewRequest(request))
			require.Nil(t, response)
			requireConnectError(t, err, test.code, test.message)
			require.Equal(t, before, fixture.state(t))
		})
	}
	require.Empty(t, fixture.og.reasons)
	require.Zero(t, fixture.publisher.calls)
}

func TestUpdateReleaseLocaleMetadataSourceLocaleIntegration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		title       *string
		notes       *intrav1.ReleaseCreditNotesValue
		changed     bool
		wantTitle   string
		wantNotes   func(f releaseLocaleMetadataFixture) map[string]string
		wantReasons []string
	}{
		{
			name:      "title is trimmed and requests OG refresh",
			title:     proto.String("  Renamed source  "),
			changed:   true,
			wantTitle: "Renamed source",
			wantNotes: func(f releaseLocaleMetadataFixture) map[string]string {
				return map[string]string{f.notedCredit: "Source note"}
			},
			wantReasons: []string{"release_source_title_saved"},
		},
		{
			name: "credit notes replace the whole source set without OG refresh",
			notes: &intrav1.ReleaseCreditNotesValue{Values: []*intrav1.ReleaseCreditNote{
				{CreditId: "PLAIN", Note: "  Added note "},
			}},
			changed:   true,
			wantTitle: releaseMetadataSourceTitle,
			wantNotes: func(f releaseLocaleMetadataFixture) map[string]string {
				return map[string]string{f.plainCredit: "Added note"}
			},
		},
		{
			name:      "empty credit note set clears source notes",
			notes:     &intrav1.ReleaseCreditNotesValue{},
			changed:   true,
			wantTitle: releaseMetadataSourceTitle,
			wantNotes: func(releaseLocaleMetadataFixture) map[string]string { return nil },
		},
		{
			name:      "unchanged title is a no-op",
			title:     proto.String(releaseMetadataSourceTitle),
			wantTitle: releaseMetadataSourceTitle,
			wantNotes: func(f releaseLocaleMetadataFixture) map[string]string {
				return map[string]string{f.notedCredit: "Source note"}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newReleaseLocaleMetadataFixture(t)
			request := fixture.request(t, "en")
			request.Title = test.title
			if test.notes != nil {
				request.CreditNotes = proto.Clone(test.notes).(*intrav1.ReleaseCreditNotesValue)
				for _, value := range request.CreditNotes.Values {
					if value.CreditId == "PLAIN" {
						value.CreditId = fixture.plainCredit
					}
				}
			}
			before := fixture.state(t)

			response, err := fixture.service.UpdateReleaseLocaleMetadata(fixture.ctx, connect.NewRequest(request))
			require.NoError(t, err)

			after := fixture.state(t)
			want := &intrav1.UpdateReleaseLocaleMetadataResponse{
				Locale: "en", DocumentRevision: after.DocumentRevision,
				Changed: test.changed, SourceChanged: test.changed,
				ChangedLocales: []string{"en"},
			}
			require.True(t, proto.Equal(want, response.Msg), "want %v\ngot %v", want, response.Msg)
			if test.changed {
				require.NotEqual(t, before.DocumentRevision, after.DocumentRevision)
				require.True(t, after.ReleaseUpdatedAt.After(before.ReleaseUpdatedAt))
			} else {
				require.Equal(t, before.DocumentRevision, after.DocumentRevision)
				require.Equal(t, before.ReleaseUpdatedAt, after.ReleaseUpdatedAt)
			}
			require.Equal(t, test.wantTitle, *after.Titles["en"])
			require.Equal(t, before.Titles["fr"], after.Titles["fr"], "a source edit leaves the target title alone")
			require.Equal(t, test.wantNotes(fixture), after.Notes["en"])
			require.Equal(t, before.Notes["fr"], after.Notes["fr"])
			if test.changed {
				require.Equal(t, []releaseAuditRow{{
					Action: string(sharedtelemetry.AuditReleaseUpdated), ChangedFields: pq.StringArray{"locale_content"},
					Locale: proto.String("en"), ItemOperation: proto.String(string(sharedtelemetry.AuditItemOperationUpdated)),
					ActorKind: "member", ActorMemberID: proto.String(fixture.contributor),
				}}, after.Audits)
			} else {
				require.Empty(t, after.Audits, "an unchanged source edit appends no Domain Audit record")
			}
			require.Equal(t, test.wantReasons, fixture.og.reasons)
			require.Zero(t, fixture.publisher.calls, "source metadata edits publish no content event")
		})
	}
}

func TestUpdateReleaseLocaleMetadataTargetLocaleIntegration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		title     *string
		notes     func(f releaseLocaleMetadataFixture) *intrav1.ReleaseCreditNotesValue
		changed   bool
		wantTitle string
		wantNotes func(f releaseLocaleMetadataFixture) map[string]string
		// wantShown is the target note display, when the case pins it.
		wantShown func(f releaseLocaleMetadataFixture) map[string]string
	}{
		{
			name:      "title is trimmed",
			title:     proto.String("  Nouveau titre "),
			changed:   true,
			wantTitle: "Nouveau titre",
			wantNotes: func(f releaseLocaleMetadataFixture) map[string]string {
				return map[string]string{f.notedCredit: "Note francaise"}
			},
		},
		{
			name:      "padded unchanged title is a no-op",
			title:     proto.String(" " + releaseMetadataTargetTitle + " "),
			wantTitle: releaseMetadataTargetTitle,
			wantNotes: func(f releaseLocaleMetadataFixture) map[string]string {
				return map[string]string{f.notedCredit: "Note francaise"}
			},
		},
		{
			// An empty target note removes the target override; the credit
			// then shows its source note.
			name: "empty credit note removes the target note",
			notes: func(f releaseLocaleMetadataFixture) *intrav1.ReleaseCreditNotesValue {
				return &intrav1.ReleaseCreditNotesValue{Values: []*intrav1.ReleaseCreditNote{
					{CreditId: f.notedCredit, Note: "  "}, {CreditId: f.plainCredit, Note: "Ajout"},
				}}
			},
			changed:   true,
			wantTitle: releaseMetadataTargetTitle,
			wantNotes: func(f releaseLocaleMetadataFixture) map[string]string {
				return map[string]string{f.plainCredit: "Ajout"}
			},
			wantShown: func(f releaseLocaleMetadataFixture) map[string]string {
				return map[string]string{f.notedCredit: "Source note", f.plainCredit: "Ajout"}
			},
		},
		{
			name: "only empty credit notes clear every target note",
			notes: func(f releaseLocaleMetadataFixture) *intrav1.ReleaseCreditNotesValue {
				return &intrav1.ReleaseCreditNotesValue{Values: []*intrav1.ReleaseCreditNote{
					{CreditId: f.notedCredit, Note: ""},
				}}
			},
			changed:   true,
			wantTitle: releaseMetadataTargetTitle,
			wantNotes: func(releaseLocaleMetadataFixture) map[string]string { return nil },
		},
		{
			name:  "credit notes replace the whole target set",
			title: nil,
			notes: func(f releaseLocaleMetadataFixture) *intrav1.ReleaseCreditNotesValue {
				return &intrav1.ReleaseCreditNotesValue{Values: []*intrav1.ReleaseCreditNote{
					{CreditId: f.plainCredit, Note: " Ajout "},
				}}
			},
			changed:   true,
			wantTitle: releaseMetadataTargetTitle,
			wantNotes: func(f releaseLocaleMetadataFixture) map[string]string {
				return map[string]string{f.plainCredit: "Ajout"}
			},
		},
		{
			name:      "unchanged title is a no-op",
			title:     proto.String(releaseMetadataTargetTitle),
			wantTitle: releaseMetadataTargetTitle,
			wantNotes: func(f releaseLocaleMetadataFixture) map[string]string {
				return map[string]string{f.notedCredit: "Note francaise"}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newReleaseLocaleMetadataFixture(t)
			request := fixture.request(t, "fr")
			request.Title = test.title
			request.ExpectedTargetRevision = proto.String(fixture.targetRevision)
			if test.notes != nil {
				request.CreditNotes = test.notes(fixture)
			}
			before := fixture.state(t)

			response, err := fixture.service.UpdateReleaseLocaleMetadata(fixture.ctx, connect.NewRequest(request))
			require.NoError(t, err)

			after := fixture.state(t)
			require.Equal(t, before.DocumentRevision, after.DocumentRevision, "target edits never advance the shared revision")
			require.Equal(t, before.ReleaseUpdatedAt, after.ReleaseUpdatedAt)
			require.Equal(t, "fr", response.Msg.GetLocale())
			require.Equal(t, after.DocumentRevision, response.Msg.GetDocumentRevision())
			require.Equal(t, test.changed, response.Msg.GetChanged())
			require.False(t, response.Msg.GetSourceChanged())
			require.NotNil(t, response.Msg.TargetRevision)
			if test.changed {
				require.Equal(t, []string{"fr"}, response.Msg.GetChangedLocales())
				require.NotEqual(t, fixture.targetRevision, response.Msg.GetTargetRevision())
				require.Equal(t, []releaseAuditRow{{
					Action: string(sharedtelemetry.AuditReleaseUpdated), ChangedFields: pq.StringArray{"locale_content"},
					Locale: proto.String("fr"), ItemOperation: proto.String(string(sharedtelemetry.AuditItemOperationUpdated)),
					ActorKind: "member", ActorMemberID: proto.String(fixture.contributor),
				}}, after.Audits)
			} else {
				require.Empty(t, response.Msg.GetChangedLocales())
				require.Equal(t, fixture.targetRevision, response.Msg.GetTargetRevision())
				require.Empty(t, after.Audits)
			}
			require.NotNil(t, after.Titles["fr"])
			require.Equal(t, test.wantTitle, *after.Titles["fr"])
			require.Equal(t, before.Titles["en"], after.Titles["en"])
			require.Equal(t, test.wantNotes(fixture), after.Notes["fr"])
			require.Equal(t, before.Notes["en"], after.Notes["en"])
			if test.wantShown != nil {
				shown, err := releasepkg.LoadReleaseCreditNotesForDisplay(
					t.Context(), fixture.db, fixture.releaseID, "en", "fr",
					[]string{fixture.notedCredit, fixture.plainCredit},
				)
				require.NoError(t, err)
				require.Equal(t, test.wantShown(fixture), shown)
			}
			require.Empty(t, fixture.og.reasons, "target edits never request OG work")
			require.Zero(t, fixture.publisher.calls)
		})
	}
}

func TestApplyReleaseBlockBatchSourceAuditIntegration(t *testing.T) {
	t.Parallel()
	fixture := newReleaseLocaleMetadataFixture(t)
	before := fixture.state(t)
	apply := func(text string) *intrav1.ApplyReleaseBlockBatchResponse {
		t.Helper()
		var blockID string
		require.NoError(t, fixture.db.Raw(
			`SELECT id::text FROM content_block WHERE document_id = ?::uuid ORDER BY id LIMIT 1`, fixture.documentID,
		).Scan(&blockID).Error)
		response, err := fixture.service.ApplyReleaseBlockBatch(fixture.ctx, connect.NewRequest(&intrav1.ApplyReleaseBlockBatchRequest{
			ReleaseId: fixture.releaseID, Locale: "en",
			Batch: testutil.NewParagraphBlockBatch(
				creativeContentIntegrationDocument("en", ""), fixture.documentRevision(t), "en", blockID, text,
				[]string{fixture.contributor},
			),
		}))
		require.NoError(t, err)
		return response.Msg
	}

	changed := apply("Edited release body")
	require.True(t, changed.GetChanged())
	sourceAudit := releaseAuditRow{
		Action: string(sharedtelemetry.AuditReleaseUpdated), ChangedFields: pq.StringArray{"locale_content"},
		Locale: proto.String("en"), ItemOperation: proto.String(string(sharedtelemetry.AuditItemOperationUpdated)),
		ActorKind: "member", ActorMemberID: proto.String(fixture.contributor),
	}
	require.Equal(t, append(before.Audits, sourceAudit), fixture.state(t).Audits)
	require.Equal(t, []string{"release_source_document_saved"}, fixture.og.reasons)

	unchanged := apply("Edited release body")
	require.False(t, unchanged.GetChanged())
	require.Equal(t, append(before.Audits, sourceAudit), fixture.state(t).Audits, "an unchanged batch appends no record")
}
