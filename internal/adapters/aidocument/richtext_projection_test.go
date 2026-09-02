package aidocumentadapter

import (
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

func TestRichTextCodecProjectsGeneratedInline(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := codec.Project(localizedParagraphDocument(uuid.New(), "before"))
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Localized[0].Value.Inline[0].Text != "before" {
		t.Fatalf("projection = %+v", nodes)
	}
}

func TestRichTextCodecProjectsEveryGeneratedProfileBlock(t *testing.T) {
	profiles := []contentv1.RichTextProfile{
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_PAGE,
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT,
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT,
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL,
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY,
	}
	for _, profile := range profiles {
		t.Run(profile.String(), func(t *testing.T) {
			codec, err := NewRichTextCodec(profile)
			if err != nil {
				t.Fatal(err)
			}
			document := &contentv1.LocalizedRichTextDocument{
				BlockCatalogFingerprint: codec.Catalog().Fingerprint,
				Profile:                 profile, Locale: "en",
				Base:          &contentv1.RichTextBlockGraph{},
				LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en"},
			}
			wantKinds := make(map[core.BlockKind]struct{}, len(codec.descriptor.Blocks))
			for _, descriptor := range codec.descriptor.Blocks {
				blockID := uuid.NewString()
				node, locale, err := codec.newBlock(core.BlockKind(descriptor.Kind), blockID)
				if err != nil {
					t.Fatalf("new %s: %v", descriptor.Kind, err)
				}
				node.Placement = &contentv1.ContentBlockPlacement{}
				document.Base.Nodes = append(document.Base.Nodes, node)
				document.LocaleOverlay.Blocks = append(document.LocaleOverlay.Blocks, locale)
				wantKinds[core.BlockKind(descriptor.Kind)] = struct{}{}
			}
			nodes, err := codec.Project(document)
			if err != nil {
				t.Fatal(err)
			}
			if len(nodes) != len(wantKinds) {
				t.Fatalf("projected %d nodes, want %d", len(nodes), len(wantKinds))
			}
			for _, node := range nodes {
				if _, ok := wantKinds[node.Kind]; !ok {
					t.Fatalf("unexpected projected kind %q", node.Kind)
				}
				delete(wantKinds, node.Kind)
			}
			if len(wantKinds) != 0 {
				t.Fatalf("unprojected generated kinds = %+v", wantKinds)
			}
		})
	}
}
