package contentblock

import (
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/sitevariable"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// projectSiteVariables operates only on the renderer's private clone.
func projectSiteVariables(document *contentv1.LocalizedRichTextDocument, contexts []sitevariable.Context) error {
	var variables *sitevariable.Context
	if len(contexts) > 0 {
		variables = &contexts[0]
	}
	var walk func(protoreflect.Message) error
	walk = func(message protoreflect.Message) error {
		if inline, ok := message.Interface().(*contentv1.RichTextInline); ok && inline.GetSiteVariable() != nil {
			if variables == nil {
				return fmt.Errorf("site variable context is required")
			}
			atom := inline.GetSiteVariable()
			text, err := variables.Resolve(atom.GetKey())
			if err != nil {
				return err
			}
			styled := &contentv1.RichTextStyledText{Text: text, Styles: atom.GetStyles()}
			inline.Value = &contentv1.RichTextInline_Text{Text: styled}
			if atom.GetLinked() && text != "" {
				href := text
				if strings.HasPrefix(atom.GetKey(), "contact.") {
					href = "mailto:" + text
				}
				if _, err := safeLink(href); err != nil {
					return err
				}
				inline.Value = &contentv1.RichTextInline_Link{Link: &contentv1.RichTextLink{Href: href, Content: []*contentv1.RichTextStyledText{styled}}}
			}
			return nil
		}
		// Link labels migrated from the legacy text-only link contract retain tokens.
		if link, ok := message.Interface().(*contentv1.RichTextLink); ok && hasSiteVariableToken(link.GetHref()) {
			if variables == nil {
				return fmt.Errorf("site variable context is required")
			}
			link.Href = variables.ResolveText(link.GetHref())
			if _, err := safeLink(link.GetHref()); err != nil {
				return err
			}
		}
		if text, ok := message.Interface().(*contentv1.RichTextStyledText); ok && variables != nil {
			text.Text = variables.ResolveText(text.GetText())
		}
		var failure error
		message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
			if field.Message() == nil || field.IsMap() {
				return true
			}
			if field.IsList() {
				list := value.List()
				for i := 0; i < list.Len(); i++ {
					if failure = walk(list.Get(i).Message()); failure != nil {
						return false
					}
				}
			} else {
				failure = walk(value.Message())
			}
			return failure == nil
		})
		return failure
	}
	return walk(document.ProtoReflect())
}

func hasSiteVariableToken(text string) bool {
	return strings.Contains(text, "{{site.") || strings.Contains(text, "{{contact.")
}

// HasSiteVariables lets owning services capture one settings context only when needed.
func HasSiteVariables(document interface{ ProtoReflect() protoreflect.Message }) bool {
	var found bool
	var walk func(protoreflect.Message)
	walk = func(message protoreflect.Message) {
		if atom, ok := message.Interface().(*contentv1.RichTextInline); ok && atom.GetSiteVariable() != nil {
			found = true
			return
		}
		if link, ok := message.Interface().(*contentv1.RichTextLink); ok && hasSiteVariableToken(link.GetHref()) {
			found = true
			return
		}
		if text, ok := message.Interface().(*contentv1.RichTextStyledText); ok && hasSiteVariableToken(text.GetText()) {
			found = true
			return
		}
		message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
			if field.Message() == nil || field.IsMap() {
				return true
			}
			if field.IsList() {
				list := value.List()
				for i := 0; i < list.Len() && !found; i++ {
					walk(list.Get(i).Message())
				}
			} else {
				walk(value.Message())
			}
			return !found
		})
	}
	walk(document.ProtoReflect())
	return found
}
