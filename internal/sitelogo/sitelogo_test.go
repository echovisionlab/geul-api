package sitelogo

import "testing"

func ptr(value string) *string { return &value }

func TestDisplayResolveFallsBackAcrossThemeThenForm(t *testing.T) {
	cases := []struct {
		name    string
		display Display[string]
		want    map[[2]int]string
	}{
		{
			name:    "all slots configured",
			display: Display[string]{FullLight: ptr("fl"), FullDark: ptr("fd"), SymbolLight: ptr("sl"), SymbolDark: ptr("sd")},
			want:    map[[2]int]string{{0, 0}: "fl", {0, 1}: "fd", {1, 0}: "sl", {1, 1}: "sd"},
		},
		{
			name:    "light only per form",
			display: Display[string]{FullLight: ptr("fl"), SymbolLight: ptr("sl")},
			want:    map[[2]int]string{{0, 0}: "fl", {0, 1}: "fl", {1, 0}: "sl", {1, 1}: "sl"},
		},
		{
			name:    "full only",
			display: Display[string]{FullDark: ptr("fd")},
			want:    map[[2]int]string{{0, 0}: "fd", {0, 1}: "fd", {1, 0}: "fd", {1, 1}: "fd"},
		},
		{
			name:    "symbol only prefers the matching theme",
			display: Display[string]{SymbolLight: ptr("sl"), SymbolDark: ptr("sd")},
			want:    map[[2]int]string{{0, 0}: "sl", {0, 1}: "sd", {1, 0}: "sl", {1, 1}: "sd"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for key, want := range tc.want {
				got := tc.display.Resolve(Form(key[0]), Theme(key[1]))
				if got == nil || *got != want {
					t.Errorf("Resolve(%d, %d) = %v, want %s", key[0], key[1], got, want)
				}
			}
		})
	}
	if got := (Display[string]{}).Resolve(Full, Light); got != nil {
		t.Errorf("empty display resolved to %v", *got)
	}
	resolved := Display[string]{SymbolDark: ptr("sd")}.Resolved()
	if *resolved.FullLight != "sd" || *resolved.FullDark != "sd" || *resolved.SymbolLight != "sd" || *resolved.SymbolDark != "sd" {
		t.Errorf("Resolved() did not fill every slot from the only logo: %+v", resolved)
	}
}

func TestEmailResolveFallsBackAcrossThemesOnly(t *testing.T) {
	if got := (Email[string]{Light: ptr("l"), Dark: ptr("d")}).Resolve(Dark); *got != "d" {
		t.Errorf("dark = %s", *got)
	}
	if got := (Email[string]{Light: ptr("l")}).Resolve(Dark); *got != "l" {
		t.Errorf("dark without dark = %s", *got)
	}
	if got := (Email[string]{Dark: ptr("d")}).Resolve(Light); *got != "d" {
		t.Errorf("light without light = %s", *got)
	}
	if got := (Email[string]{}).Resolve(Light); got != nil {
		t.Errorf("empty email resolved to %v", *got)
	}
}
