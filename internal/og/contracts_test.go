package og

import "testing"

func TestTargetCanonicalLocale(t *testing.T) {
	alias := "zh_Hant"
	invalid := "xx-YY"
	tests := []struct {
		name    string
		locale  *string
		want    string
		wantErr bool
	}{
		{name: "absent"},
		{name: "alias", locale: &alias, want: "zh-TW"},
		{name: "invalid", locale: &invalid, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := (Target{Locale: test.locale}).CanonicalLocale()
			if (err != nil) != test.wantErr {
				t.Fatalf("CanonicalLocale() error = %v, wantErr %t", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("CanonicalLocale() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPrepareBulkOgRequestsCanonicalizesDurableLocaleTarget(t *testing.T) {
	t.Parallel()
	alias := "zh_Hant"
	prepared, err := prepareBulkOgRequests([]Request{{
		Target: Target{EntityType: "post", EntityID: "post-1", Locale: &alias, Kind: "locale"},
	}})
	if err != nil {
		t.Fatalf("prepareBulkOgRequests() error = %v", err)
	}
	if len(prepared) != 1 || prepared[0].locale == nil || *prepared[0].locale != "zh-TW" {
		t.Fatalf("prepared locale = %#v, want zh-TW", prepared)
	}
	if prepared[0].targetKey != "post\x00post-1\x00zh-TW" {
		t.Fatalf("target key = %q, want canonical locale key", prepared[0].targetKey)
	}
}

func TestPrepareBulkOgRequestsRejectsUnsupportedLocaleTarget(t *testing.T) {
	t.Parallel()
	unsupported := "xx-YY"
	_, err := prepareBulkOgRequests([]Request{{
		Target: Target{EntityType: "post", EntityID: "post-1", Locale: &unsupported, Kind: "locale"},
	}})
	if err == nil {
		t.Fatal("prepareBulkOgRequests() accepted unsupported locale")
	}
}
