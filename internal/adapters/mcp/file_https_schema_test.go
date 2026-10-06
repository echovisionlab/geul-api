package mcp

import (
	"encoding/json"
	"regexp"
	"testing"
)

func TestFileHTTPSInputSchemasMatchCompleteURLs(t *testing.T) {
	var upload struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(fileUploadInputJSONSchema), &upload); err != nil {
		t.Fatal(err)
	}
	var file struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(upload.Properties["file"], &file); err != nil {
		t.Fatal(err)
	}
	var transfer struct {
		OneOf []struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"oneOf"`
	}
	if err := json.Unmarshal(fileTransferInputSchema(), &transfer); err != nil {
		t.Fatal(err)
	}
	if len(transfer.OneOf) < 2 {
		t.Fatal("remote HTTPS transfer schema is missing")
	}

	for _, schema := range []struct {
		name     string
		property json.RawMessage
	}{
		{name: "file_upload.download_url", property: file.Properties["download_url"]},
		{name: "file_transfer.remote_https.u", property: transfer.OneOf[1].Properties["u"]},
	} {
		t.Run(schema.name, func(t *testing.T) {
			var property struct {
				Pattern   string `json:"pattern"`
				Format    string `json:"format"`
				MaxLength int    `json:"maxLength"`
			}
			if err := json.Unmarshal(schema.property, &property); err != nil {
				t.Fatal(err)
			}
			if property.Format != "uri" || property.MaxLength != 4096 {
				t.Fatalf("URL constraints changed: %+v", property)
			}
			// Exercise full-string matching as a compatibility check; JSON
			// Schema does not implicitly anchor regular expressions.
			pattern, err := regexp.Compile(`\A(?:` + property.Pattern + `)\z`)
			if err != nil {
				t.Fatal(err)
			}
			for _, input := range []struct {
				url  string
				want bool
			}{
				{url: "https://example.com/file.pdf", want: true},
				{url: "https://files.example.oaiusercontent.com/files/fixture.pdf?se=2026-10-06T12%3A00%3A00Z&sig=fixture%2Bvalue%3D", want: true},
				{url: ""},
				{url: "https://"},
				{url: "http://example.com/file.pdf"},
				{url: "ftp://example.com/file.pdf"},
				{url: "data:application/pdf;base64,fixture"},
			} {
				if got := pattern.MatchString(input.url); got != input.want {
					t.Errorf("pattern %q matches %q = %v, want %v", property.Pattern, input.url, got, input.want)
				}
			}
		})
	}
}
