package artist

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestBuildArtistLocaleContentUpdatedEventUsesExactLocaleFence(t *testing.T) {
	targetRevision := "target-token"
	event := buildArtistLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID:         "artist-1",
		Fields:           []string{"content", "content", "title"},
		DocumentRevision: "document-revision",
		Contributors:     []string{"member-1"},
		Source:           managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
		Locale:           " ko ",
		TargetRevision:   &targetRevision,
	})
	if event == nil {
		t.Fatal("target event is nil")
	}
	if event.GetLocale() != "ko" || !event.GetLocaleExists() || event.GetTargetRevision() != targetRevision || event.GetDocumentStateChanged() {
		t.Fatalf("target locale fence = %+v", event)
	}
	if event.GetDocumentRevision() != "document-revision" || len(event.GetChangedFields()) != 2 ||
		event.GetChangedFields()[0].GetPath() != "document.content" || event.GetChangedFields()[1].GetPath() != "title" {
		t.Fatalf("target event fields = %+v", event.GetChangedFields())
	}

	source := buildArtistLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID:             "artist-1",
		Fields:               []string{"content"},
		DocumentRevision:     "document-revision",
		Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		Locale:               "en",
		DocumentStateChanged: true,
	})
	if source == nil || !source.GetDocumentStateChanged() || source.GetLocale() != "en" || !source.GetLocaleExists() || source.TargetRevision != nil {
		t.Fatalf("source event = %+v", source)
	}
}

func TestBuildArtistLocaleContentUpdatedEventRejectsInvalidLocaleFence(t *testing.T) {
	targetRevision := "target-token"
	cases := []struct {
		name    string
		exists  bool
		target  *string
		changed bool
	}{
		{name: "target with shared revision change", exists: true, target: &targetRevision, changed: true},
		{name: "target without target revision", exists: true, changed: false},
		{name: "deleted target with target revision", target: &targetRevision},
		{name: "deleted target with shared revision change", changed: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if event := buildArtistLocaleContentUpdatedEvent(contentsignal.LocaleChange{
				EntityID:             "artist-1",
				Fields:               []string{"content"},
				DocumentRevision:     "document-revision",
				Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
				Locale:               "ko",
				LocaleDeleted:        !test.exists,
				TargetRevision:       test.target,
				DocumentStateChanged: test.changed,
			}); event != nil {
				t.Fatalf("invalid locale fence event = %+v", event)
			}
		})
	}
}
