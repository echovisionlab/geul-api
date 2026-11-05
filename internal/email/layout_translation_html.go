package email

import (
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/translation"
)

var layoutHTMLTranslatableAttributes = map[string]struct{}{
	"alt":        {},
	"aria-label": {},
	"title":      {},
}

// BuildLayoutTranslationExtractionPlan extracts only stable visible-text and
// accessibility-attribute units from a canonical Email Layout source wrapper.
func BuildLayoutTranslationExtractionPlan(
	layoutID string,
	sourceLocale string,
	targetLocale string,
	source *translation.SourceDocument,
) (*translation.ExtractionPlan, error) {
	if source == nil {
		return nil, fmt.Errorf("translation source document is required")
	}
	content := dereferenceString(source.ContentHTML)
	if strings.TrimSpace(content) == "" {
		return nil, translation.ErrNoTranslatableUnits
	}
	descriptors, err := ExtractLayoutContentUnits(content)
	if err != nil {
		return nil, err
	}
	if len(descriptors) == 0 {
		return nil, translation.ErrNoTranslatableUnits
	}

	units := make([]translation.Unit, 0, len(descriptors))
	for _, descriptor := range descriptors {
		context := nonEmptyStringPointer(descriptor.Context)
		field := "content_html"
		if descriptor.Attribute != "" {
			field = descriptor.Attribute
		}
		units = append(units, translation.Unit{
			UnitID:        descriptor.Handle,
			EntityType:    "email_layout",
			EntityID:      layoutID,
			Path:          "entity:content_html:" + descriptor.Handle,
			ContainerType: translation.ContainerTypeHTMLNode,
			ContainerID:   descriptor.Handle,
			FieldName:     field,
			SourceText:    descriptor.SourceValue,
			SourceFormat:  descriptor.SourceFormat,
			SourceLocale:  sourceLocale,
			Context:       context,
		})
	}
	return &translation.ExtractionPlan{
		EntityType:   "email_layout",
		EntityID:     layoutID,
		SourceLocale: sourceLocale,
		TargetLocale: targetLocale,
		Units:        units,
		Bundles: translation.BuildBundles(
			"email_layout", layoutID, sourceLocale, targetLocale, units, nil,
		),
	}, nil
}

func popLayoutHTMLElementStack(stack []string, tag string) []string {
	if len(stack) == 0 {
		return stack
	}
	for index := len(stack) - 1; index >= 0; index-- {
		if stack[index] == tag {
			return stack[:index]
		}
	}
	return stack[:len(stack)-1]
}

func nonEmptyStringPointer(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	copy := value
	return &copy
}

func dereferenceString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
