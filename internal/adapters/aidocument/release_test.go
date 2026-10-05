package aidocumentadapter

import (
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

func TestNewReleaseRegistrationRequiresOwningService(t *testing.T) {
	if _, err := NewReleaseRegistration(nil); err == nil {
		t.Fatal("NewReleaseRegistration(nil) succeeded")
	}
}

func TestCompileReleaseCreditNotePreservesExplicitEmpty(t *testing.T) {
	creditID := "019c89aa-6798-7a37-8532-11e03f729c35"
	mutation := releasedomain.AIDocumentMutation{}
	handled, issue := compileReleaseCreditNote(&mutation, core.SetFieldOperation(
		releaseCreditBlockID(creditID), releaseCreditNoteField, core.Text(""),
	), 3)
	if !handled || issue != nil {
		t.Fatalf("compileReleaseCreditNote = (%v, %+v)", handled, issue)
	}
	if note, exists := mutation.CreditNotePatch[creditID]; !exists || note != "" {
		t.Fatalf("credit note = (%q, %v), want explicit empty", note, exists)
	}
}

func TestCompileReleaseCreditRejectsMembershipMutation(t *testing.T) {
	mutation := releasedomain.AIDocumentMutation{}
	handled, issue := compileReleaseCreditNote(&mutation, core.DeleteBlockOperation(
		releaseCreditBlockID("019c89aa-6798-7a37-8532-11e03f729c35"),
	), 0)
	if !handled || issue == nil || issue.Code != core.IssueInvalidOperation {
		t.Fatalf("membership mutation = (%v, %+v)", handled, issue)
	}
}

func TestCompileReleaseTitleUsesExactLocaleMetadata(t *testing.T) {
	mutation := releasedomain.AIDocumentMutation{}
	handled, issue := compileReleaseTitle(&mutation, core.SetFieldOperation(
		releaseMetadataBlockID, releaseTitleField, core.Text("Titre"),
	), 1)
	if !handled || issue != nil || !mutation.SetTitle || mutation.Title == nil || *mutation.Title != "Titre" {
		t.Fatalf("title mutation = (%v, %+v, %+v)", handled, issue, mutation)
	}
}

func TestReleaseProjectionKeepsSourceAndRequestedMetadataDistinct(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT)
	if err != nil {
		t.Fatal(err)
	}
	releaseID, documentID, blockID := uuid.New(), uuid.New(), uuid.New()
	sourceTitle, targetTitle := "Source", "Cible"
	state := releasedomain.AIDocumentState{
		ReleaseID: releaseID.String(), DocumentID: documentID,
		DocumentRevision: uuid.NewString(), SourceLocale: "en", Locale: "fr", LocaleExists: true,
		TargetRevision: stringPointer("tr1_target"), Document: exactCompactDocument(blockID, "fr"),
		CreditIDs:         []string{uuid.NewString()},
		SourceMetadata:    releasedomain.AIDocumentLocaleMetadata{Title: &sourceTitle, CreditNotes: map[string]string{}},
		RequestedMetadata: &releasedomain.AIDocumentLocaleMetadata{Title: &targetTitle, CreditNotes: map[string]string{}},
	}
	document, err := (&releasePort{codec: codec, catalog: releaseCatalog(codec)}).document(
		core.DocumentIdentity{Domain: core.DomainRelease, Reference: core.DocumentReference(releaseID.String())}, "fr", state,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Nodes) == 0 || len(document.Nodes[0].Localized) != 1 || document.Nodes[0].Localized[0].Value.Text != targetTitle {
		t.Fatalf("requested metadata projection = %+v", document.Nodes)
	}
}

func TestReleaseIdentityFailsClosed(t *testing.T) {
	for _, identity := range []core.DocumentIdentity{
		{Domain: core.DomainArtist, Reference: "019c89aa-6798-7a37-8532-11e03f729c35"},
		{Domain: core.DomainRelease, Reference: "019C89AA-6798-7A37-8532-11E03F729C35"},
	} {
		if err := validateReleaseDocumentIdentity(identity); err == nil {
			t.Fatalf("validateReleaseDocumentIdentity(%+v) succeeded", identity)
		}
	}
}
