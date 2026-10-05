package mcp

import (
	"fmt"
	"reflect"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func TestTranslationLocalesPreserveExplicitOrderAndRejectImplicitOrDuplicateTargets(t *testing.T) {
	locales, err := translationLocales([]core.Locale{"en", "ja"})
	if err != nil {
		t.Fatalf("translationLocales() error = %v", err)
	}
	if !reflect.DeepEqual(locales, []string{"en", "ja"}) {
		t.Fatalf("translationLocales() = %v", locales)
	}
	for _, input := range [][]core.Locale{nil, {"en", "en"}} {
		if _, err := translationLocales(input); err == nil {
			t.Fatalf("translationLocales(%v) succeeded", input)
		}
	}
}

func TestTranslationJobStatusInputUsesResponseVocabulary(t *testing.T) {
	for name, status := range translationJobStatusesByName {
		if got := translationJobStatuses[status]; got != name {
			t.Fatalf("status round trip %q -> %v -> %q", name, status, got)
		}
	}
}

func TestTranslationJobSortAcceptsSupportedFields(t *testing.T) {
	for _, field := range []string{"requested_at", "updated_at", "target_locale", "status"} {
		if !validTranslationJobSort(field) {
			t.Fatalf("validTranslationJobSort(%q) = false", field)
		}
	}
	if validTranslationJobSort("unknown") {
		t.Fatal("unknown Translation Job sort was accepted")
	}
}

func TestTranslationJobSortDirectionRequiresExplicitField(t *testing.T) {
	for _, arguments := range []string{`{"z":false}`, `{"z":true}`} {
		var input translationJobsListArguments
		if err := decodeArguments(toolArguments(t, arguments), &input); err != nil {
			t.Fatal(err)
		}
		if _, err := translationJobsListRequest(input); err == nil {
			t.Fatalf("sort direction without its field was accepted: %s", arguments)
		}
	}
	for _, test := range []struct {
		arguments string
		order     commonv1.SortOrder
	}{
		{`{"k":"status"}`, commonv1.SortOrder_SORT_ORDER_ASC},
		{`{"k":"status","z":false}`, commonv1.SortOrder_SORT_ORDER_ASC},
		{`{"k":"status","z":true}`, commonv1.SortOrder_SORT_ORDER_DESC},
	} {
		var input translationJobsListArguments
		if err := decodeArguments(toolArguments(t, test.arguments), &input); err != nil {
			t.Fatal(err)
		}
		request, err := translationJobsListRequest(input)
		if err != nil || len(request.GetSorts()) != 1 || request.Sorts[0].Order != test.order {
			t.Fatalf("sort request for %s = %+v, %v", test.arguments, request, err)
		}
	}
}

func TestTranslationLocaleTagShapeRejectsEmptySubtags(t *testing.T) {
	for _, locale := range []core.Locale{"en", "zh-CN", "es-419", "pt-PT"} {
		if err := validateCompactLocale(locale); err != nil {
			t.Fatalf("supported tag shape %q rejected: %v", locale, err)
		}
	}
	for _, locale := range []core.Locale{"en--US", "-en", "en-", "en_US", "en US", ""} {
		if err := validateCompactLocale(locale); err == nil {
			t.Fatalf("invalid tag shape %q accepted", locale)
		}
	}
}

func TestTranslationLocalesRejectSelectionAboveAdvertisedMaximum(t *testing.T) {
	locales := make([]core.Locale, 33)
	for index := range locales {
		locales[index] = core.Locale(fmt.Sprintf("tag%d", index))
	}
	if _, err := translationLocales(locales[:32]); err != nil {
		t.Fatalf("maximum selection rejected: %v", err)
	}
	if _, err := translationLocales(locales); err == nil {
		t.Fatal("locale selection above the advertised maximum was accepted")
	}
}
