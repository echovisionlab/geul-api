// email-preview-fixture renders a single Storybook draft through production email
// rendering. It reads JSON on stdin and writes JSON on stdout; it has no server,
// database, credentials, network client, or mail delivery setup.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/echovisionlab/geul-api/internal/email"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

const maxDraftInputBytes = 32 << 20

type draftRequest struct {
	Document    json.RawMessage   `json:"document"`
	ContentHTML *string           `json:"contentHtml"`
	Subject     string            `json:"subject"`
	LayoutHTML  string            `json:"layoutHtml"`
	Variables   map[string]string `json:"variables"`
	Locale      string            `json:"locale"`
}

type draftResponse struct {
	Subject string `json:"subject"`
	HTML    string `json:"html"`
	Text    string `json:"text"`
}

func main() {
	// Imported renderers may diagnose invalid input; fixture stderr exposes only
	// the bounded transport errors below, never document or variable contents.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := renderFixture(context.Background(), os.Stdin, os.Stdout); err != nil {
		// Do not print parser errors, document content, or template variables.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func renderFixture(ctx context.Context, input io.Reader, output io.Writer) error {
	body, err := io.ReadAll(io.LimitReader(input, maxDraftInputBytes+1))
	if err != nil {
		return errors.New("cannot read email draft input")
	}
	if len(body) > maxDraftInputBytes {
		return errors.New("email draft input exceeds 32 MiB")
	}
	if trimmed := bytes.TrimSpace(body); len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("email draft must contain one JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request draftRequest
	if err := decoder.Decode(&request); err != nil {
		return errors.New("invalid email draft JSON")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("email draft must contain one JSON object")
	}
	layout, err := email.PrepareDraftLayoutHTML(request.LayoutHTML)
	if err != nil {
		return errors.New("invalid email draft layout")
	}
	draft := email.DraftEmail{
		Subject: request.Subject, LayoutHTML: layout,
		Variables: request.Variables, Locale: request.Locale,
	}
	if len(request.Document) != 0 && !bytes.Equal(bytes.TrimSpace(request.Document), []byte("null")) {
		if request.ContentHTML != nil {
			return errors.New("email draft cannot contain both document and contentHtml")
		}
		draft.Document = &contentv1.LocalizedRichTextDocument{}
		if err := protojson.Unmarshal(request.Document, draft.Document); err != nil {
			return errors.New("invalid email draft document")
		}
	} else if request.ContentHTML != nil {
		draft.ContentHTML = *request.ContentHTML
	}
	rendered, err := email.RenderDraftEmail(ctx, draft)
	if err != nil {
		return errors.New("email draft rendering failed")
	}
	if err := json.NewEncoder(output).Encode(draftResponse{Subject: rendered.Subject, HTML: rendered.HTML, Text: rendered.Text}); err != nil {
		return errors.New("cannot write email draft response")
	}
	return nil
}
