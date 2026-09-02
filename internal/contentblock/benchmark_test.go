package contentblock

import (
	"encoding/json"
	"fmt"
	"testing"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
)

func BenchmarkGeneratedContentBlockFoundation(b *testing.B) {
	for _, count := range []int{1, 10, 100, 1_000} {
		state := benchmarkAggregate(b, count)
		contract := NewGeneratedContract()
		b.Run(fmt.Sprintf("SnapshotDigest/%d", count), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if _, err := snapshotDigest(state); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("Validate/%d", count), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				candidate := state.clone()
				if err := validateAggregate(contract, &candidate, "en"); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("SingleLocaleEdit/%d", count), func(b *testing.B) {
			first := orderedBlocks(state.blocks)[0].ID
			edited := benchmarkLocalizedJSON(b, "edited")
			b.ReportAllocs()
			for range b.N {
				candidate := state.clone()
				candidate.locales[first]["en"] = edited
				if err := validateAggregate(contract, &candidate, "en"); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("FullReorder/%d", count), func(b *testing.B) {
			ordered := orderedBlocks(state.blocks)
			b.ReportAllocs()
			for range b.N {
				candidate := state.clone()
				for index, block := range ordered {
					value := candidate.blocks[block.ID]
					value.Position = count - index - 1
					candidate.blocks[block.ID] = value
				}
				if err := validateAggregate(contract, &candidate, "en"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchmarkAggregate(b *testing.B, count int) aggregate {
	b.Helper()
	state := newAggregate(Document{Profile: "post"})
	shared := benchmarkSharedJSON(b)
	localized := benchmarkLocalizedJSON(b, "A realistic short paragraph")
	for index := range count {
		blockID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("benchmark-%d", index)))
		state.blocks[blockID] = FullBlock{BaseBlock: BaseBlock{
			ID:            blockID,
			ContainerSlot: "content",
			Position:      index,
			Kind:          "paragraph",
			SharedData:    append(json.RawMessage(nil), shared...),
		}}
		state.locales[blockID] = map[string]json.RawMessage{
			"en": append(json.RawMessage(nil), localized...),
		}
	}
	return state
}

func benchmarkSharedJSON(b *testing.B) json.RawMessage {
	b.Helper()
	value, err := protojson.Marshal(&contentv1.RichTextBlockData{Value: &contentv1.RichTextBlockData_Paragraph{
		Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
	}})
	if err != nil {
		b.Fatal(err)
	}
	return value
}

func benchmarkLocalizedJSON(b *testing.B, text string) json.RawMessage {
	b.Helper()
	value, err := protojson.Marshal(&contentv1.RichTextBlockLocaleData{Value: &contentv1.RichTextBlockLocaleData_Paragraph{
		Paragraph: &contentv1.ParagraphBlockLocale{
			Props: &contentv1.ParagraphLocaleProps{},
			Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
				Text: &contentv1.RichTextStyledText{Text: text},
			}}},
		},
	}})
	if err != nil {
		b.Fatal(err)
	}
	return value
}
