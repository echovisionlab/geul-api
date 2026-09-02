package programevent

import "testing"

func TestNormalizeRequiredProgramEventLocale(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "canonical", input: "ko", want: "ko"},
		{name: "alias", input: "zh_Hant", want: "zh-TW"},
		{name: "unsupported", input: "xx-YY", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeRequiredProgramEventLocale("locale", test.input)
			if (err != nil) != test.wantErr {
				t.Fatalf("normalizeRequiredProgramEventLocale() error = %v, wantErr %t", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("normalizeRequiredProgramEventLocale() = %q, want %q", got, test.want)
			}
		})
	}
}
