package application

import (
	"strconv"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"
)

func TestTranslationOverviewSQLUsesCompleteTargetCatalog(t *testing.T) {
	t.Parallel()
	valuesSQL := translationOverviewEntityValuesSQL()
	unionSQL := translationOverviewEntryUnionSQL()
	for index, definition := range translation.Definitions() {
		value := "('" + string(definition.Kind) + "', " + strconv.Itoa(index+1) + ")"
		if !strings.Contains(valuesSQL, value) {
			t.Fatalf("overview values omit %s", definition.Kind)
		}
		entryQuery := "FROM " + definition.EntryTable
		if !strings.Contains(unionSQL, entryQuery) {
			t.Fatalf("overview union omits %s", definition.EntryTable)
		}
	}
	if strings.Contains(valuesSQL, "site_setting") || strings.Contains(unionSQL, "site_setting") {
		t.Fatal("overview SQL includes legacy Site Settings target")
	}
}
