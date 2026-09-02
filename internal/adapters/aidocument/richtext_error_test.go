package aidocumentadapter

import (
	"strings"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

func TestRichTextCodecGeneratedValidationRejectsMalformedMarkParameter(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	if err != nil {
		t.Fatal(err)
	}
	blockID := uuid.New()
	operation := core.SetFieldOperation(core.BlockID(blockID.String()), "content", core.RichText(
		core.TextColor("RGB", core.InlineText("invalid")),
	))
	if _, err := inlineToGenerated(operation.SetField.Value.Inline); err != nil {
		t.Fatalf("representation conversion duplicated generated color policy: %v", err)
	}
	_, issues, err := codec.Compile(uuid.New(), localizedParagraphDocument(blockID, "before"), core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(), []core.Operation{operation})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Operation != -1 || issues[0].Code != core.IssueInvalidOperation ||
		!strings.Contains(issues[0].Message, "flatten Rich Text mutation") || !strings.Contains(issues[0].Message, "invalid editor color") {
		t.Fatalf("issues = %+v", issues)
	}
}

func TestRichTextCodecRejectsInlineMathOutsideGeneratedProfileCapability(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT)
	if err != nil {
		t.Fatal(err)
	}
	blockID := uuid.New()
	document := localizedParagraphDocument(blockID, "before")
	document.Profile = contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT
	operation := core.SetFieldOperation(
		core.BlockID(blockID.String()),
		"content",
		core.RichText(core.InlineMath("x")),
	)
	if _, err := inlineToGenerated(operation.SetField.Value.Inline); err != nil {
		t.Fatalf("representation conversion duplicated generated profile policy: %v", err)
	}

	_, issues, err := codec.Compile(
		uuid.New(),
		document,
		core.LocaleRoleSource,
		core.Revision(uuid.NewString()),
		uuid.New(),
		[]core.Operation{operation},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Operation != -1 || issues[0].Code != core.IssueInvalidOperation ||
		!strings.Contains(issues[0].Message, "flatten Rich Text mutation") || !strings.Contains(issues[0].Message, "inline math is forbidden") {
		t.Fatalf("issues = %+v", issues)
	}
}

func TestRichTextCodecEnforcesEveryGeneratedProfileInlineCapability(t *testing.T) {
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
			items := []core.InlineItem{core.InlineText("plain"), core.HardBreak()}
			for _, mark := range codec.descriptor.Marks {
				child := core.InlineText(mark.Name)
				switch mark.Name {
				case "bold":
					items = append(items, core.Bold(child))
				case "italic":
					items = append(items, core.Italic(child))
				case "underline":
					items = append(items, core.Underline(child))
				case "strike":
					items = append(items, core.Strike(child))
				case "code":
					items = append(items, core.InlineCode(child))
				case "textColor":
					items = append(items, core.TextColor("#aabbcc", child))
				case "backgroundColor":
					items = append(items, core.BackgroundColor("default", child))
				case "link":
					items = append(items, core.Link("https://www.geul.example", child))
				default:
					t.Fatalf("test has no generated mark adapter for %q", mark.Name)
				}
			}
			if codec.descriptor.InlineMath {
				items = append(items, core.InlineMath("x"))
			}
			blockID := uuid.New()
			document := localizedParagraphDocument(blockID, "before")
			document.Profile = profile
			_, issues, err := codec.Compile(
				uuid.New(), document, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(),
				[]core.Operation{core.SetFieldOperation(core.BlockID(blockID.String()), "content", core.RichText(items...))},
			)
			if err != nil || len(issues) != 0 {
				t.Fatalf("generated capabilities compile = (%+v, %v)", issues, err)
			}
		})
	}
}

func TestRichTextCodecGeneratedValidatorRejectsEveryUnsupportedProfileInlineCapability(t *testing.T) {
	profiles := []contentv1.RichTextProfile{
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_PAGE,
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT,
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT,
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL,
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY,
	}
	markCases := map[string]core.InlineItem{
		"bold":            core.Bold(core.InlineText("value")),
		"italic":          core.Italic(core.InlineText("value")),
		"underline":       core.Underline(core.InlineText("value")),
		"strike":          core.Strike(core.InlineText("value")),
		"code":            core.InlineCode(core.InlineText("value")),
		"textColor":       core.TextColor("#aabbcc", core.InlineText("value")),
		"backgroundColor": core.BackgroundColor("yellow", core.InlineText("value")),
		"link":            core.Link("https://www.geul.example", core.InlineText("value")),
	}
	rejections := 0
	for _, profile := range profiles {
		codec, err := NewRichTextCodec(profile)
		if err != nil {
			t.Fatal(err)
		}
		allowed := make(map[string]struct{}, len(codec.descriptor.Marks))
		for _, mark := range codec.descriptor.Marks {
			allowed[mark.Name] = struct{}{}
		}
		for mark, item := range markCases {
			if _, ok := allowed[mark]; ok {
				continue
			}
			t.Run(profile.String()+"/"+mark, func(t *testing.T) {
				rejections++
				assertGeneratedInlineProfileRejection(t, codec, profile, item)
			})
		}
		if !codec.descriptor.InlineMath {
			t.Run(profile.String()+"/math", func(t *testing.T) {
				rejections++
				assertGeneratedInlineProfileRejection(t, codec, profile, core.InlineMath("x"))
			})
		}
	}
	if rejections == 0 {
		t.Fatal("generated profiles exposed no negative inline capability coverage")
	}
}

func assertGeneratedInlineProfileRejection(t *testing.T, codec *RichTextCodec, profile contentv1.RichTextProfile, item core.InlineItem) {
	t.Helper()
	if _, err := inlineToGenerated([]core.InlineItem{item}); err != nil {
		t.Fatalf("representation conversion duplicated generated profile policy: %v", err)
	}
	blockID := uuid.New()
	document := localizedParagraphDocument(blockID, "before")
	document.Profile = profile
	_, issues, err := codec.Compile(
		uuid.New(), document, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(),
		[]core.Operation{core.SetFieldOperation(core.BlockID(blockID.String()), "content", core.RichText(item))},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Operation != -1 || issues[0].Code != core.IssueInvalidOperation ||
		!strings.Contains(issues[0].Message, "flatten Rich Text mutation") || !strings.Contains(issues[0].Message, "profile") {
		t.Fatalf("generated profile issues = %+v", issues)
	}
}

func TestRichTextCodecRejectsUnknownStoredEnumWithoutPanicking(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	if err != nil {
		t.Fatal(err)
	}
	blockID := uuid.NewString()
	document := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: codec.Catalog().Fingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		Locale:                  "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Shader{Shader: &contentv1.ShaderBlock{Props: &contentv1.ShaderProps{Stages: []*contentv1.ShaderProps_StagesItem{{
				Kind: contentv1.ShaderProps_StagesItem_Kind(999),
			}}}}}},
			Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en"},
	}
	if _, err := codec.Project(document); err == nil || !strings.Contains(err.Error(), "unknown stored enum number 999") {
		t.Fatalf("unknown enum error = %v", err)
	}
}
