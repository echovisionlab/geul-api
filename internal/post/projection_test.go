package post

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestManagePostProjectionIncludesSourceLocale(t *testing.T) {
	for _, locale := range []string{"ko", "en", "pt-BR", "zh-TW"} {
		t.Run(locale, func(t *testing.T) {
			service := &PostService{}
			response := service.toProtoPost(&model.Post{
				SourceLocale: locale, DocumentLayout: model.DefaultDocumentLayout(),
			}, nil)
			if response.GetSourceLocale() != locale {
				t.Fatalf("source locale = %q, want %q", response.GetSourceLocale(), locale)
			}
			// Exercise the wire response consumed before the editor snapshot RPC.
			wire, err := protojson.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			decoded := new(managev1.Post)
			if err := protojson.Unmarshal(wire, decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.GetSourceLocale() != locale {
				t.Fatalf("wire source locale missing: %s", wire)
			}
		})
	}
}
