//go:build integration

package localization

import (
	"reflect"
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestRuntimeCatalogMatchesCanonicalLocalesIntegration(t *testing.T) {
	postgres := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{
		ApplyAppSchemaSQL: true,
	})
	locales, err := NewCatalog(postgres.DB).All(t.Context())
	if err != nil {
		t.Fatalf("load runtime locale catalog: %v", err)
	}

	codes := make([]string, 0, len(locales))
	for _, locale := range locales {
		codes = append(codes, locale.Code)
	}
	want := canonicalLocaleCodes[:]
	if !reflect.DeepEqual(codes, want) {
		t.Fatalf("runtime locale codes = %v, want canonical registry %v", codes, want)
	}
}
