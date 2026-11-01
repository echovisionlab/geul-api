package legal_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/legal/legaltest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// harness is one Terms and Privacy use-case test: the three services over
// the fake store, its unit of work and the fakes of every other port.
type harness struct {
	store     *legaltest.Store
	units     *mutationtest.UnitOfWork[legal.Store]
	checker   *authztest.Checker
	fence     *identitystatetest.Fence
	audit     *audittest.Recorder
	dispatch  *dispatch
	ogAssets  *ogAssets
	service   *legal.Service
	internal  *legal.InternalService
	documents *legal.AIDocumentService
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store, units := legaltest.New()
	h := &harness{
		store: store, units: units, checker: authztest.AllowAll(), fence: &identitystatetest.Fence{},
		audit: &audittest.Recorder{}, dispatch: &dispatch{}, ogAssets: &ogAssets{},
	}
	var err error
	h.service, err = legal.NewService(legal.Deps{
		Units: units, SpiceDB: h.checker, Fence: h.fence, Audit: h.audit,
		Dispatch: h.dispatch, OGAssets: h.ogAssets, BaseURL: "https://example.test",
	})
	require.NoError(t, err)
	h.internal, err = legal.NewInternalService(legal.InternalDeps{Units: units, SpiceDB: h.checker, Fence: h.fence, Audit: h.audit})
	require.NoError(t, err)
	h.documents, err = legal.NewAIDocumentService(legal.AIDocumentDeps{Units: units, SpiceDB: h.checker, Fence: h.fence, Audit: h.audit})
	require.NoError(t, err)
	return h
}

// dispatch records the post-commit notice steps.
type dispatch struct {
	mu         sync.Mutex
	dispatched []string
	cancelled  []string
	err        error
}

func (d *dispatch) DispatchRun(_ context.Context, runID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dispatched = append(d.dispatched, runID)
	return d.err
}

func (d *dispatch) CancelUpdateNoticeAfterActivation(_ context.Context, kind legal.PolicyKind, id string, _ time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cancelled = append(d.cancelled, string(kind)+"/"+id)
}

// ogAssets records the OG image releases of deleted versions.
type ogAssets struct {
	released []string
	err      error
}

func (o *ogAssets) ReleaseOGAssets(_ context.Context, kind legal.PolicyKind, id string) error {
	o.released = append(o.released, string(kind)+"/"+id)
	return o.err
}

// principal is a signed-in Member.
type principal struct {
	identityID string
	memberID   string
}

func newPrincipal() principal {
	return principal{identityID: uuid.NewString(), memberID: uuid.NewString()}
}

func (p principal) user() *auth.UserInfo {
	return &auth.UserInfo{
		IdentityID: auth.IdentityID(p.identityID), MemberID: auth.MemberID(p.memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	}
}

// context carries p and the request correlation a request audit needs.
func (p principal) context(t *testing.T) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.80")
	require.NoError(t, err)
	ctx := sharedtelemetry.WithRequestContext(t.Context(), requestContext)
	ctx = sharedtelemetry.WithActor(ctx, sharedtelemetry.MemberActor{IdentityID: p.identityID, MemberID: p.memberID, SessionID: uuid.NewString()})
	return auth.WithUser(ctx, p.user())
}

// kinds are both policy kinds; most scenarios run on each.
var kinds = []legal.PolicyKind{legal.Terms, legal.Privacy}

func status(kind legal.PolicyKind, state string) legal.Status {
	stored, ok := legal.PolicyStatus(kind, state)
	if !ok {
		panic("unknown legal status " + state)
	}
	return stored
}

// notFoundResource is the kind's wording in a version's NotFound answer.
func notFoundResource(kind legal.PolicyKind) string {
	if kind == legal.Privacy {
		return "privacy policy"
	}
	return "terms"
}

// policyDocument is a one-paragraph policy document in sourceLocale.
func policyDocument(sourceLocale, text string) *contentv1.RichTextDocument {
	blockID := uuid.NewString()
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY,
		SourceLocale:            sourceLocale,
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{
				Id:    blockID,
				Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}},
			},
			Placement: &contentv1.ContentBlockPlacement{Index: 0},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{
			Locale: sourceLocale,
			Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: blockID,
				Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
					Props: &contentv1.ParagraphLocaleProps{},
					Content: []*contentv1.RichTextInline{{
						Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: text}},
					}},
				}},
			}},
		}},
	}
}

// putVersion stores a version of kind in state with a one-paragraph
// English document and returns it with its document revision.
func (h *harness) putVersion(t *testing.T, kind legal.PolicyKind, state string, change func(*legal.Version)) (legal.Version, string) {
	t.Helper()
	version := legal.Version{Status: status(kind, state)}
	if change != nil {
		change(&version)
	}
	version = h.store.PutVersion(kind, version, "en")
	documentID := uuid.MustParse(*version.ContentDocumentID)
	replace, err := contentblock.ReplaceFromRichTextProto(documentID, uuid.New(), policyDocument("en", "Body of "+version.Title))
	require.NoError(t, err)
	document := legaltest.Document{ID: documentID, Profile: "policy", Revision: uuid.New(), Blocks: replace.Blocks, Overlays: map[string][]contentblock.LocaleBlockUpdate{}}
	for _, overlay := range replace.LocaleOverlays {
		document.Overlays[overlay.Locale] = overlay.Blocks
	}
	h.store.PutDocument(document)
	return version, document.Revision.String()
}

// revision is the current document revision of a version.
func (h *harness) revision(t *testing.T, kind legal.PolicyKind, id string) string {
	t.Helper()
	record, ok := h.store.Record(kind, id)
	require.True(t, ok)
	document, ok := h.store.Document(uuid.MustParse(*record.ContentDocumentID))
	require.True(t, ok)
	return document.Revision.String()
}

// stored reads a version back from the fake.
func (h *harness) stored(t *testing.T, kind legal.PolicyKind, id string) legaltest.Record {
	t.Helper()
	record, ok := h.store.Record(kind, id)
	require.True(t, ok, "version %s is stored", id)
	return record
}

// requireAnswer asserts the Connect code and message err answers with.
func requireAnswer(t *testing.T, err error, code connect.Code, message string) {
	t.Helper()
	require.Error(t, err)
	answer := errs.Wrap(err)
	require.Equal(t, code, answer.Code(), answer.Message())
	if message != "" {
		require.Equal(t, message, answer.Message())
	}
}

// actions lists the audit actions recorded, in order.
func (h *harness) actions() []string {
	var actions []string
	for _, record := range h.audit.Records() {
		actions = append(actions, string(record.Action))
	}
	return actions
}

var errStore = errors.New("store unavailable")
