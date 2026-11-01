package release_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/release/releasetest"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const (
	metadataSourceTitle = "Source title"
	metadataTargetTitle = "Titre"
)

// metadataFixture is an English-source Release with two credits (only the
// first carries a source note) and an explicit French target locale.
type metadataFixture struct {
	*harness
	handler        *release.InternalConnectHandler
	releaseID      string
	notedCredit    string
	plainCredit    string
	targetRevision string
}

func newMetadataFixture(t *testing.T) metadataFixture {
	t.Helper()
	h := newHarness(t)
	value := h.seedRelease(t, metadataSourceTitle)
	noted, plain := uuid.NewString(), uuid.NewString()
	h.store.AddReleaseCredit(value.ID, release.ReleaseCredit{ID: noted, CreditedName: text("Noted"), SortOrder: 0})
	h.store.AddReleaseCredit(value.ID, release.ReleaseCredit{ID: plain, CreditedName: text("Plain"), SortOrder: 1})
	updatedAt := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	h.store.PutLocale(value.ID, release.LocaleMetadata{Locale: "en", Title: text(metadataSourceTitle), UpdatedAt: updatedAt}, map[string]string{noted: "Source note"})
	h.store.PutLocale(value.ID, release.LocaleMetadata{Locale: "fr", Title: text(metadataTargetTitle), UpdatedAt: updatedAt}, map[string]string{noted: "Note francaise"})
	state, err := h.service.LoadAIDocumentState(h.ctx(t), release.AIDocumentStateQuery{ReleaseID: value.ID, Locale: "fr"})
	require.NoError(t, err)
	require.NotNil(t, state.TargetRevision)
	return metadataFixture{
		harness: h, handler: release.NewInternalConnectHandler(h.service), releaseID: value.ID,
		notedCredit: noted, plainCredit: plain, targetRevision: *state.TargetRevision,
	}
}

func (f metadataFixture) request(t *testing.T, locale string) *intrav1.UpdateReleaseLocaleMetadataRequest {
	t.Helper()
	return &intrav1.UpdateReleaseLocaleMetadataRequest{
		ReleaseId: f.releaseID, Locale: locale, ExpectedRevision: f.revision(t, f.releaseID).String(),
		ContributorMemberIds: []string{f.principal.memberID},
	}
}

// metadataState is everything a locale metadata save may write.
type metadataState struct {
	Titles   map[string]*string
	Notes    map[string]map[string]string
	Revision uuid.UUID
	Updated  time.Time
	Audits   int
	Updates  int
	OG       int
}

func (f metadataFixture) state(t *testing.T) metadataState {
	t.Helper()
	state := metadataState{Titles: map[string]*string{}, Notes: map[string]map[string]string{}, Revision: f.revision(t, f.releaseID)}
	for _, locale := range []string{"en", "fr", "ko"} {
		if metadata, notes, ok := f.store.Locale(f.releaseID, locale); ok {
			state.Titles[locale] = metadata.Title
			if len(notes) != 0 {
				state.Notes[locale] = notes
			}
		}
	}
	value, _ := f.store.Release(f.releaseID)
	state.Updated = value.UpdatedAt
	state.Audits, state.Updates, state.OG = len(f.audit.Records()), len(f.store.Updates()), len(f.store.OGRequests())
	return state
}

func creditNotes(values map[string]string) *intrav1.ReleaseCreditNotesValue {
	notes := &intrav1.ReleaseCreditNotesValue{}
	for creditID, note := range values {
		notes.Values = append(notes.Values, &intrav1.ReleaseCreditNote{CreditId: creditID, Note: note})
	}
	return notes
}

func TestUpdateReleaseLocaleMetadataRejectionsLeaveStateUnchanged(t *testing.T) {
	fixture := newMetadataFixture(t)
	foreignCredit, unknownRelease := uuid.NewString(), uuid.NewString()
	stale := "stale-target-revision"
	tests := []struct {
		name    string
		mutate  func(*intrav1.UpdateReleaseLocaleMetadataRequest)
		code    connect.Code
		message string
	}{
		{"no metadata value", func(*intrav1.UpdateReleaseLocaleMetadataRequest) {}, connect.CodeInvalidArgument, "update: at least one metadata value is required"},
		{"non-canonical locale", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) { r.Locale, r.Title = "english", proto.String("Title") }, connect.CodeInvalidArgument, "locale: must be an exact canonical locale"},
		{"expected revision is not a UUID", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) {
			r.ExpectedRevision, r.Title = "not-a-uuid", proto.String("Title")
		}, connect.CodeInvalidArgument, "expected_revision: must be a UUID"},
		{"expected revision is the nil UUID", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) {
			r.ExpectedRevision, r.Title = uuid.Nil.String(), proto.String("Title")
		}, connect.CodeInvalidArgument, "expected_revision: must be a UUID"},
		{"unknown release", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) {
			r.ReleaseId, r.Title = unknownRelease, proto.String("Title")
		}, connect.CodeNotFound, "release not found: " + unknownRelease},
		{"no contributors", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) {
			r.ContributorMemberIds, r.Title = nil, proto.String("Title")
		}, connect.CodeInvalidArgument, "contributor_member_ids: collaboration mutation requires contributors"},
		{"unknown contributor", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) {
			r.ContributorMemberIds, r.Title = []string{uuid.NewString()}, proto.String("Title")
		}, connect.CodeInvalidArgument, "contributor_member_ids: contains a Member that does not exist"},
		{"null credit note entry", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) {
			r.CreditNotes = &intrav1.ReleaseCreditNotesValue{Values: []*intrav1.ReleaseCreditNote{nil}}
		}, connect.CodeInvalidArgument, "credit_notes: must not contain null entries"},
		{"blank credit id", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) {
			r.CreditNotes = creditNotes(map[string]string{" ": "Note"})
		}, connect.CodeInvalidArgument, "credit_notes: credit_id is required"},
		{"empty source credit note", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) {
			r.CreditNotes = creditNotes(map[string]string{fixture.notedCredit: "  "})
		}, connect.CodeInvalidArgument, "credit_notes: note must not be empty"},
		{"credit of another release", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) {
			r.CreditNotes = creditNotes(map[string]string{foreignCredit: "Note"})
		}, connect.CodeInvalidArgument, "credit_notes: credit_id does not belong to the Release"},
		{"duplicate credit id", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) {
			r.CreditNotes = &intrav1.ReleaseCreditNotesValue{Values: []*intrav1.ReleaseCreditNote{
				{CreditId: fixture.notedCredit, Note: "One"}, {CreditId: " " + fixture.notedCredit, Note: "Two"},
			}}
		}, connect.CodeInvalidArgument, "credit_notes: credit_id must be unique"},
		{"source room carries a target revision", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) {
			r.Title, r.ExpectedTargetRevision = proto.String("Title"), proto.String(fixture.targetRevision)
		}, connect.CodeInvalidArgument, "expected_target_revision: source Release room cannot carry a target revision"},
		{"blank source title", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) { r.Title = proto.String("   ") }, connect.CodeInvalidArgument, "title: must not be empty"},
		{"stale source document revision", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) {
			r.ExpectedRevision, r.Title = uuid.NewString(), proto.String("Title")
		}, connect.CodeFailedPrecondition, "release content revision changed; reload before saving"},
		{"stale target document revision", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) {
			r.Locale, r.ExpectedRevision = "fr", uuid.NewString()
			r.Title, r.ExpectedTargetRevision = proto.String("Title"), proto.String(fixture.targetRevision)
		}, connect.CodeFailedPrecondition, "release content revision changed; reload before saving"},
		{"stale target revision", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) {
			r.Locale, r.Title, r.ExpectedTargetRevision = "fr", proto.String("Title"), &stale
		}, connect.CodeFailedPrecondition, "translation target revision conflict"},
		{"existing target without target revision", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) {
			r.Locale, r.Title = "fr", proto.String("Title")
		}, connect.CodeFailedPrecondition, "translation target revision conflict"},
		{"target locale not created yet", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) {
			r.Locale, r.Title = "ko", proto.String("Title")
		}, connect.CodeFailedPrecondition, "Release target locale must be explicitly created before collaboration"},
		{"target note for credit of another release", func(r *intrav1.UpdateReleaseLocaleMetadataRequest) {
			r.Locale, r.ExpectedTargetRevision = "fr", proto.String(fixture.targetRevision)
			r.CreditNotes = creditNotes(map[string]string{foreignCredit: "Note"})
		}, connect.CodeInvalidArgument, "credit_notes: credit_id does not belong to the Release"},
	}
	before := fixture.state(t)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := fixture.request(t, "en")
			test.mutate(request)
			response, err := fixture.handler.UpdateReleaseLocaleMetadata(fixture.ctx(t), connect.NewRequest(request))
			require.Nil(t, response)
			requireCode(t, err, test.code, test.message)
			require.Equal(t, before, fixture.state(t))
		})
	}
}

func TestUpdateReleaseLocaleMetadataSourceLocale(t *testing.T) {
	tests := []struct {
		name        string
		title       *string
		notes       func(f metadataFixture) *intrav1.ReleaseCreditNotesValue
		changed     bool
		wantTitle   string
		wantNotes   func(f metadataFixture) map[string]string
		wantReasons []string
	}{
		{
			name: "title is trimmed and requests OG refresh", title: proto.String("  Renamed source  "),
			changed: true, wantTitle: "Renamed source",
			wantNotes:   func(f metadataFixture) map[string]string { return map[string]string{f.notedCredit: "Source note"} },
			wantReasons: []string{"release_source_title_saved"},
		},
		{
			name: "credit notes replace the whole source set without OG refresh",
			notes: func(f metadataFixture) *intrav1.ReleaseCreditNotesValue {
				return creditNotes(map[string]string{f.plainCredit: "  Added note "})
			},
			changed: true, wantTitle: metadataSourceTitle,
			wantNotes: func(f metadataFixture) map[string]string { return map[string]string{f.plainCredit: "Added note"} },
		},
		{
			name:    "empty credit note set clears source notes",
			notes:   func(metadataFixture) *intrav1.ReleaseCreditNotesValue { return &intrav1.ReleaseCreditNotesValue{} },
			changed: true, wantTitle: metadataSourceTitle,
			wantNotes: func(metadataFixture) map[string]string { return nil },
		},
		{
			name: "unchanged title is a no-op", title: proto.String(metadataSourceTitle), wantTitle: metadataSourceTitle,
			wantNotes: func(f metadataFixture) map[string]string { return map[string]string{f.notedCredit: "Source note"} },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMetadataFixture(t)
			request := fixture.request(t, "en")
			request.Title = test.title
			if test.notes != nil {
				request.CreditNotes = test.notes(fixture)
			}
			before := fixture.state(t)

			response, err := fixture.handler.UpdateReleaseLocaleMetadata(fixture.ctx(t), connect.NewRequest(request))
			require.NoError(t, err)

			after := fixture.state(t)
			want := &intrav1.UpdateReleaseLocaleMetadataResponse{
				Locale: "en", DocumentRevision: after.Revision.String(), Changed: test.changed, SourceChanged: test.changed,
				ChangedLocales: []string{"en"},
			}
			require.True(t, proto.Equal(want, response.Msg), "want %v\ngot %v", want, response.Msg)
			if test.changed {
				require.NotEqual(t, before.Revision, after.Revision)
				require.True(t, after.Updated.After(before.Updated))
				records := fixture.audit.Records()
				require.Len(t, records, 1)
				require.Equal(t, sharedtelemetry.AuditReleaseUpdated, records[0].Action)
				require.Equal(t, "en", records[0].Locale)
				require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, records[0].ItemOperation)
				require.Equal(t, fixture.principal.memberID, records[0].MemberID)
			} else {
				require.Equal(t, before.Revision, after.Revision)
				require.Equal(t, before.Updated, after.Updated)
				require.Empty(t, fixture.audit.Records(), "an unchanged source edit appends no Domain Audit record")
			}
			require.Equal(t, test.wantTitle, *after.Titles["en"])
			require.Equal(t, before.Titles["fr"], after.Titles["fr"], "a source edit leaves the target title alone")
			require.Equal(t, test.wantNotes(fixture), after.Notes["en"])
			require.Equal(t, before.Notes["fr"], after.Notes["fr"])
			var reasons []string
			for _, request := range fixture.store.OGRequests() {
				reasons = append(reasons, request.Reason)
			}
			require.Equal(t, test.wantReasons, reasons)
			require.Empty(t, fixture.store.Updates(), "source metadata edits publish no content event")
		})
	}
}

func TestUpdateReleaseLocaleMetadataTargetLocale(t *testing.T) {
	tests := []struct {
		name      string
		title     *string
		notes     func(f metadataFixture) *intrav1.ReleaseCreditNotesValue
		changed   bool
		wantTitle string
		wantNotes func(f metadataFixture) map[string]string
		wantShown func(f metadataFixture) map[string]string
	}{
		{
			name: "title is trimmed", title: proto.String("  Nouveau titre "), changed: true, wantTitle: "Nouveau titre",
			wantNotes: func(f metadataFixture) map[string]string { return map[string]string{f.notedCredit: "Note francaise"} },
		},
		{
			name: "padded unchanged title is a no-op", title: proto.String(" " + metadataTargetTitle + " "), wantTitle: metadataTargetTitle,
			wantNotes: func(f metadataFixture) map[string]string { return map[string]string{f.notedCredit: "Note francaise"} },
		},
		{
			// An empty target note removes the target override; the credit
			// then shows its source note.
			name: "empty credit note removes the target note",
			notes: func(f metadataFixture) *intrav1.ReleaseCreditNotesValue {
				return &intrav1.ReleaseCreditNotesValue{Values: []*intrav1.ReleaseCreditNote{
					{CreditId: f.notedCredit, Note: "  "}, {CreditId: f.plainCredit, Note: "Ajout"},
				}}
			},
			changed: true, wantTitle: metadataTargetTitle,
			wantNotes: func(f metadataFixture) map[string]string { return map[string]string{f.plainCredit: "Ajout"} },
			wantShown: func(f metadataFixture) map[string]string {
				return map[string]string{f.notedCredit: "Source note", f.plainCredit: "Ajout"}
			},
		},
		{
			name: "only empty credit notes clear every target note",
			notes: func(f metadataFixture) *intrav1.ReleaseCreditNotesValue {
				return creditNotes(map[string]string{f.notedCredit: ""})
			},
			changed: true, wantTitle: metadataTargetTitle,
			wantNotes: func(metadataFixture) map[string]string { return nil },
		},
		{
			name: "credit notes replace the whole target set",
			notes: func(f metadataFixture) *intrav1.ReleaseCreditNotesValue {
				return creditNotes(map[string]string{f.plainCredit: " Ajout "})
			},
			changed: true, wantTitle: metadataTargetTitle,
			wantNotes: func(f metadataFixture) map[string]string { return map[string]string{f.plainCredit: "Ajout"} },
		},
		{
			name: "unchanged title is a no-op", title: proto.String(metadataTargetTitle), wantTitle: metadataTargetTitle,
			wantNotes: func(f metadataFixture) map[string]string { return map[string]string{f.notedCredit: "Note francaise"} },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMetadataFixture(t)
			request := fixture.request(t, "fr")
			request.Title = test.title
			request.ExpectedTargetRevision = proto.String(fixture.targetRevision)
			if test.notes != nil {
				request.CreditNotes = test.notes(fixture)
			}
			before := fixture.state(t)

			response, err := fixture.handler.UpdateReleaseLocaleMetadata(fixture.ctx(t), connect.NewRequest(request))
			require.NoError(t, err)

			after := fixture.state(t)
			require.Equal(t, before.Revision, after.Revision, "target edits never advance the shared revision")
			require.Equal(t, before.Updated, after.Updated)
			require.Equal(t, "fr", response.Msg.GetLocale())
			require.Equal(t, after.Revision.String(), response.Msg.GetDocumentRevision())
			require.Equal(t, test.changed, response.Msg.GetChanged())
			require.False(t, response.Msg.GetSourceChanged())
			require.NotNil(t, response.Msg.TargetRevision)
			if test.changed {
				require.Equal(t, []string{"fr"}, response.Msg.GetChangedLocales())
				require.NotEqual(t, fixture.targetRevision, response.Msg.GetTargetRevision())
				records := fixture.audit.Records()
				require.Len(t, records, 1)
				require.Equal(t, "fr", records[0].Locale)
				require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, records[0].ItemOperation)
				require.Equal(t, fixture.principal.memberID, records[0].MemberID)
			} else {
				require.Empty(t, response.Msg.GetChangedLocales())
				require.Equal(t, fixture.targetRevision, response.Msg.GetTargetRevision())
				require.Empty(t, fixture.audit.Records())
			}
			require.Equal(t, test.wantTitle, *after.Titles["fr"])
			require.Equal(t, before.Titles["en"], after.Titles["en"])
			require.Equal(t, test.wantNotes(fixture), after.Notes["fr"])
			require.Equal(t, before.Notes["en"], after.Notes["en"])
			if test.wantShown != nil {
				require.Equal(t, test.wantShown(fixture), release.MergeCreditNotesForDisplay(
					after.Notes["en"], after.Notes["fr"], []string{fixture.notedCredit, fixture.plainCredit},
				))
			}
			require.Empty(t, fixture.store.OGRequests(), "target edits never request OG work")
			require.Empty(t, fixture.store.Updates())
		})
	}
}

// Two target locales keep independent revisions: saving one leaves the
// other's revision valid, and deleting a target never advances the shared
// document revision.
func TestReleaseTargetLocaleRevisionsAreIndependent(t *testing.T) {
	fixture := newMetadataFixture(t)
	ctx := fixture.ctx(t)
	updatedAt := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	fixture.store.PutLocale(fixture.releaseID, release.LocaleMetadata{Locale: "ja", Title: text("Taitoru"), UpdatedAt: updatedAt}, nil)
	japanese, err := fixture.service.LoadAIDocumentState(ctx, release.AIDocumentStateQuery{ReleaseID: fixture.releaseID, Locale: "ja"})
	require.NoError(t, err)

	request := fixture.request(t, "fr")
	request.Title, request.ExpectedTargetRevision = proto.String("Nouveau"), proto.String(fixture.targetRevision)
	saved, err := fixture.handler.UpdateReleaseLocaleMetadata(ctx, connect.NewRequest(request))
	require.NoError(t, err)
	require.NotEqual(t, fixture.targetRevision, saved.Msg.GetTargetRevision())

	japaneseAfter, err := fixture.service.LoadAIDocumentState(ctx, release.AIDocumentStateQuery{ReleaseID: fixture.releaseID, Locale: "ja"})
	require.NoError(t, err)
	require.Equal(t, *japanese.TargetRevision, *japaneseAfter.TargetRevision, "a French save leaves the Japanese revision valid")

	revision := fixture.revision(t, fixture.releaseID)
	result, err := fixture.service.ExecuteAIDocumentMutation(ctx, release.ExecuteAIDocumentMutationCommand{
		ReleaseID: fixture.releaseID, Locale: "ja", Mode: release.AIDocumentExecutionApply,
		Compiler: func(state release.AIDocumentState) (release.AIDocumentMutation, error) {
			return release.AIDocumentMutation{
				ReleaseID: state.ReleaseID, Locale: state.Locale, ExpectedDocumentRevision: state.DocumentRevision,
				ExpectedTargetRevision: state.TargetRevision, ExpectedSource: state.SourceLocale,
				ExpectedPresence: state.LocaleExists, ContributorMemberID: uuid.MustParse(fixture.principal.memberID),
				DeleteTranslation: true,
			}, nil
		},
	})
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, revision.String(), result.DocumentRevision)
	require.Equal(t, revision, fixture.revision(t, fixture.releaseID), "deleting a target keeps the shared revision")
	_, _, exists := fixture.store.Locale(fixture.releaseID, "ja")
	require.False(t, exists)
	records := fixture.audit.Records()
	require.Equal(t, sharedtelemetry.AuditItemOperationDeleted, records[len(records)-1].ItemOperation)
}

// An AI document source edit that empties a credit note removes it, as a
// target edit does.
func TestReleaseSourceAIDocumentEmptyCreditNoteRemovesNote(t *testing.T) {
	fixture := newMetadataFixture(t)
	result, err := fixture.service.ExecuteAIDocumentMutation(fixture.ctx(t), release.ExecuteAIDocumentMutationCommand{
		ReleaseID: fixture.releaseID, Locale: "en", Mode: release.AIDocumentExecutionApply,
		Compiler: func(state release.AIDocumentState) (release.AIDocumentMutation, error) {
			return release.AIDocumentMutation{
				ReleaseID: state.ReleaseID, Locale: state.Locale, ExpectedDocumentRevision: state.DocumentRevision,
				ExpectedSource: state.SourceLocale, ExpectedPresence: state.LocaleExists,
				ContributorMemberID: uuid.MustParse(fixture.principal.memberID),
				CreditNotePatch:     map[string]string{fixture.notedCredit: "  "},
			}, nil
		},
	})
	require.NoError(t, err)
	require.True(t, result.Changed)
	_, notes, _ := fixture.store.Locale(fixture.releaseID, "en")
	require.Empty(t, notes)
	require.Len(t, fixture.store.Updates(), 1)
	require.Equal(t, release.SourceAI, fixture.store.Updates()[0].Source)
	require.Equal(t, []release.ChangedField{{Path: "document.credits", Kind: release.FieldText}}, fixture.store.Updates()[0].Fields)
	require.Equal(t, []releasetest.OGRequest{{ReleaseID: fixture.releaseID, Reason: "release_ai_document_saved"}}, fixture.store.OGRequests())
}

// Validating an AI document mutation runs its whole path and rolls it back.
func TestReleaseAIDocumentValidationRollsBack(t *testing.T) {
	fixture := newMetadataFixture(t)
	before := fixture.state(t)
	result, err := fixture.service.ExecuteAIDocumentMutation(fixture.ctx(t), release.ExecuteAIDocumentMutationCommand{
		ReleaseID: fixture.releaseID, Locale: "en", Mode: release.AIDocumentExecutionValidate,
		Compiler: func(state release.AIDocumentState) (release.AIDocumentMutation, error) {
			return release.AIDocumentMutation{
				ReleaseID: state.ReleaseID, Locale: state.Locale, ExpectedDocumentRevision: state.DocumentRevision,
				ExpectedSource: state.SourceLocale, ExpectedPresence: state.LocaleExists,
				ContributorMemberID: uuid.MustParse(fixture.principal.memberID),
				SetTitle:            true, Title: text("Validated title"),
			}, nil
		},
	})
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, mutation.StepRollback, lastStep(t, fixture.units))
	after := fixture.state(t)
	before.Audits, after.Audits = 0, 0
	require.Equal(t, before, after)
}

func paragraphBatch(expectedRevision, locale, blockID, body string, contributors []string) *contentv1.RichTextBlockMutationBatch {
	return &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Profile: contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT,
		ExpectedRevision: expectedRevision, ContributorMemberIds: contributors,
		BaseMutations: []*contentv1.RichTextBlockMutation{{Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
			Node: &contentv1.RichTextBlockNode{
				Block:     &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}}},
				Placement: &contentv1.ContentBlockPlacement{Index: 0},
			},
		}}}},
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{Locale: locale, Mutations: []*contentv1.RichTextBlockLocaleMutation{{
			Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{Block: &contentv1.RichTextBlockLocale{
				BlockId: blockID, Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
					Props:   &contentv1.ParagraphLocaleProps{},
					Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: body}}}},
				}},
			}}},
		}}}},
	}
}

func TestApplyReleaseBlockBatchSourceAudit(t *testing.T) {
	fixture := newMetadataFixture(t)
	blockID := uuid.NewString()
	apply := func(ctx context.Context) *intrav1.ApplyReleaseBlockBatchResponse {
		t.Helper()
		response, err := fixture.handler.ApplyReleaseBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyReleaseBlockBatchRequest{
			ReleaseId: fixture.releaseID, Locale: "en",
			Batch: paragraphBatch(fixture.revision(t, fixture.releaseID).String(), "en", blockID, "Edited release body", []string{fixture.principal.memberID}),
		}))
		require.NoError(t, err)
		return response.Msg
	}

	changed := apply(fixture.ctx(t))
	require.True(t, changed.GetChanged())
	require.True(t, changed.GetSourceChanged())
	records := fixture.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditReleaseUpdated, records[0].Action)
	require.Equal(t, "en", records[0].Locale)
	require.Equal(t, fixture.principal.memberID, records[0].MemberID)
	require.Equal(t, []releasetest.OGRequest{{ReleaseID: fixture.releaseID, Reason: "release_source_document_saved"}}, fixture.store.OGRequests())

	fixture.store.QueueContentResult(contentblock.Result{DocumentRevision: fixture.revision(t, fixture.releaseID)})
	unchanged := apply(fixture.ctx(t))
	require.False(t, unchanged.GetChanged())
	require.Len(t, fixture.audit.Records(), 1, "an unchanged batch appends no record")
	require.Len(t, fixture.store.OGRequests(), 1)
}

func TestApplyReleaseBlockBatchRejectsRevokedContributors(t *testing.T) {
	fixture := newMetadataFixture(t)
	revoked := connect.NewError(connect.CodePermissionDenied, nil)
	fixture.store.RevokeContributors(revoked)
	before := fixture.state(t)
	_, err := fixture.handler.ApplyReleaseBlockBatch(fixture.ctx(t), connect.NewRequest(&intrav1.ApplyReleaseBlockBatchRequest{
		ReleaseId: fixture.releaseID, Locale: "en",
		Batch: paragraphBatch(fixture.revision(t, fixture.releaseID).String(), "en", uuid.NewString(), "Body", []string{fixture.principal.memberID}),
	}))
	requireCode(t, err, connect.CodePermissionDenied, "")
	require.Equal(t, before, fixture.state(t))
	steps := fixture.units.Steps()
	require.Equal(t, []mutation.Step{mutation.StepLock, mutation.StepAccess, mutation.StepRollback}, steps[len(steps)-3:])
}
