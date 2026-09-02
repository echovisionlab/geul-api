package translation

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

const (
	// XLIFFVersion is the only interchange version accepted by Geul.
	XLIFFVersion = "2.2"
	// XLIFFNamespace is the XLIFF 2.2 Core document namespace.
	XLIFFNamespace = "urn:oasis:names:tc:xliff:document:2.2"
)

// XLIFFDocument is the provider-facing representation of one exact source
// document and target locale. Domain persistence remains the content authority.
type XLIFFDocument struct {
	Version      string
	SourceLocale string
	TargetLocale string
	File         XLIFFFile
}

// XLIFFFile represents one owning domain root.
type XLIFFFile struct {
	ID     string
	Groups []XLIFFGroup
}

// XLIFFGroup preserves semantic context and ordering for related units.
type XLIFFGroup struct {
	ID              string
	ContextTitle    *string
	ContextBefore   *string
	ContextAfter    *string
	SequenceIndex   int
	SequenceTotal   int
	TranslationUnit []XLIFFUnit
}

// XLIFFUnit is one linguistic segment with an optional translated target.
type XLIFFUnit struct {
	ID            string
	Name          string
	FieldName     string
	SourceFormat  string
	ContainerType string
	ContainerID   string
	Context       *string
	Source        string
	Target        *string
	OriginalData  []XLIFFOriginalData
	SourceInline  []XLIFFInline
	TargetInline  []XLIFFInline
}

// XLIFFOriginalData owns the immutable native representation referenced by an
// inline code. Values commonly contain a placeholder token or paired markup.
type XLIFFOriginalData struct {
	ID    string
	Value string
}

type XLIFFInlineKind string

const (
	XLIFFInlineText        XLIFFInlineKind = "text"
	XLIFFInlinePlaceholder XLIFFInlineKind = "ph"
	XLIFFInlinePairedCode  XLIFFInlineKind = "pc"
)

// XLIFFInline is a typed XLIFF 2.2 inline node. Paired codes recursively own
// their children, so nested rich-text markup round-trips without flattening.
type XLIFFInline struct {
	Kind         XLIFFInlineKind
	Text         string
	ID           string
	DataRef      string
	DataRefStart string
	DataRefEnd   string
	CanCopy      string
	CanDelete    string
	Children     []XLIFFInline
}

// BuildXLIFFDocument converts an owning extraction plan into the constrained
// XLIFF 2.2 document used by every provider adapter.
func BuildXLIFFDocument(plan *ExtractionPlan) (*XLIFFDocument, error) {
	if plan == nil {
		return nil, fmt.Errorf("translation extraction plan is required")
	}
	if strings.TrimSpace(plan.EntityType) == "" || strings.TrimSpace(plan.EntityID) == "" {
		return nil, fmt.Errorf("translation extraction plan target is required")
	}
	if strings.TrimSpace(plan.SourceLocale) == "" || strings.TrimSpace(plan.TargetLocale) == "" {
		return nil, fmt.Errorf("translation extraction plan locales are required")
	}
	if len(plan.Bundles) == 0 {
		return nil, ErrNoTranslatableUnits
	}

	document := &XLIFFDocument{
		Version:      XLIFFVersion,
		SourceLocale: plan.SourceLocale,
		TargetLocale: plan.TargetLocale,
		File: XLIFFFile{
			ID: plan.EntityType + ":" + plan.EntityID,
		},
	}
	document.File.Groups = make([]XLIFFGroup, 0, len(plan.Bundles))
	globalTitle := plan.ContextTitle
	if globalTitle == nil {
		globalTitle = xliffContextTitle(plan.Units)
	}
	for _, bundle := range plan.Bundles {
		group := XLIFFGroup{
			ID:              bundle.BundleID,
			ContextTitle:    xliffGroupContextTitle(bundle, globalTitle),
			ContextBefore:   bundle.ContextText,
			SequenceIndex:   bundle.SequenceIndex,
			SequenceTotal:   bundle.SequenceTotal,
			TranslationUnit: make([]XLIFFUnit, 0, len(bundle.Units)),
		}
		for _, unit := range bundle.Units {
			group.TranslationUnit = append(group.TranslationUnit, XLIFFUnit{
				ID: unit.UnitID, Name: unit.Path, FieldName: unit.FieldName,
				SourceFormat: unit.SourceFormat, ContainerType: unit.ContainerType,
				ContainerID: unit.ContainerID, Context: unit.Context, Source: unit.SourceText,
				OriginalData: unit.OriginalData, SourceInline: unit.SourceInline,
			})
		}
		document.File.Groups = append(document.File.Groups, group)
	}
	if err := ValidateXLIFFDocument(document, false); err != nil {
		return nil, err
	}
	return document, nil
}

func xliffGroupContextTitle(bundle Bundle, fallback *string) *string {
	for _, unit := range bundle.Units {
		if unit.FieldName == "title" {
			return xliffStringPointer(strings.TrimSpace(unit.SourceText))
		}
	}
	return fallback
}

func xliffContextTitle(units []Unit) *string {
	for _, unit := range units {
		if unit.FieldName == "title" {
			return xliffStringPointer(strings.TrimSpace(unit.SourceText))
		}
	}
	return nil
}

func xliffStringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// ValidateXLIFFDocument validates the constrained Geul provider profile. When
// requireTargets is true, every unit must contain a target and retain the
// source placeholders and line-break structure. An explicitly empty source
// retains an explicitly empty target as a stable no-content unit.
func ValidateXLIFFDocument(document *XLIFFDocument, requireTargets bool) error {
	return validateXLIFFDocument(document, requireTargets, false)
}

// ValidateXLIFFInterchangeDocument validates a human/CAT interchange document.
// Every exported unit must carry an explicit target on import, but an empty
// target remains a value rather than being treated as an omitted unit.
func ValidateXLIFFInterchangeDocument(document *XLIFFDocument) error {
	return validateXLIFFDocument(document, true, true)
}

func validateXLIFFDocument(document *XLIFFDocument, requireTargets, allowEmptyTargets bool) error {
	if document == nil {
		return fmt.Errorf("XLIFF document is required")
	}
	if document.Version != XLIFFVersion {
		return fmt.Errorf("unsupported XLIFF version %q", document.Version)
	}
	if strings.TrimSpace(document.SourceLocale) == "" || strings.TrimSpace(document.TargetLocale) == "" {
		return fmt.Errorf("XLIFF source and target locales are required")
	}
	if strings.EqualFold(strings.TrimSpace(document.SourceLocale), strings.TrimSpace(document.TargetLocale)) {
		return fmt.Errorf("XLIFF source and target locales must differ")
	}
	if strings.TrimSpace(document.File.ID) == "" {
		return fmt.Errorf("XLIFF file id is required")
	}
	if len(document.File.Groups) == 0 {
		return ErrNoTranslatableUnits
	}

	groupIDs := make(map[string]struct{}, len(document.File.Groups))
	unitIDs := make(map[string]struct{})
	for _, group := range document.File.Groups {
		if strings.TrimSpace(group.ID) == "" {
			return fmt.Errorf("XLIFF group id is required")
		}
		if _, duplicate := groupIDs[group.ID]; duplicate {
			return fmt.Errorf("duplicate XLIFF group id %q", group.ID)
		}
		groupIDs[group.ID] = struct{}{}
		if len(group.TranslationUnit) == 0 {
			return fmt.Errorf("XLIFF group %q must contain at least one unit", group.ID)
		}
		for _, unit := range group.TranslationUnit {
			if strings.TrimSpace(unit.ID) == "" {
				return fmt.Errorf("XLIFF unit id is required")
			}
			if _, duplicate := unitIDs[unit.ID]; duplicate {
				return fmt.Errorf("duplicate XLIFF unit id %q", unit.ID)
			}
			unitIDs[unit.ID] = struct{}{}
			if unit.Target == nil {
				if !requireTargets {
					if err := validateXLIFFUnitInline(unit, false); err != nil {
						return fmt.Errorf("XLIFF unit %q: %w", unit.ID, err)
					}
					continue
				}
				return fmt.Errorf("XLIFF unit %q target is required", unit.ID)
			}
			sourceEmpty := strings.TrimSpace(unit.Source) == ""
			targetEmpty := strings.TrimSpace(*unit.Target) == ""
			if targetEmpty && !allowEmptyTargets && !sourceEmpty {
				return fmt.Errorf("XLIFF unit %q target is required", unit.ID)
			}
			if !SamePlaceholders(unit.Source, *unit.Target) {
				return fmt.Errorf("XLIFF unit %q placeholder set changed", unit.ID)
			}
			if !targetEmpty && CountLineBreaks(unit.Source) != CountLineBreaks(*unit.Target) {
				return fmt.Errorf("XLIFF unit %q line-break structure changed", unit.ID)
			}
			if err := validateXLIFFUnitInline(unit, true); err != nil {
				return fmt.Errorf("XLIFF unit %q: %w", unit.ID, err)
			}
		}
	}
	return nil
}

// XLIFFTargets returns translated targets indexed by stable unit identity.
func XLIFFTargets(document XLIFFDocument) map[string]UnitResult {
	targets := make(map[string]UnitResult)
	for _, group := range document.File.Groups {
		for _, unit := range group.TranslationUnit {
			if unit.Target != nil {
				targets[unit.ID] = UnitResult{
					UnitID: unit.ID, TranslatedText: *unit.Target,
					OriginalData: unit.OriginalData, TargetInline: unit.TargetInline,
				}
			}
		}
	}
	return targets
}

// XLIFFWithTargets copies the source document and applies only known targets.
func XLIFFWithTargets(source XLIFFDocument, targets map[string]UnitResult) XLIFFDocument {
	result := source
	result.File.Groups = make([]XLIFFGroup, len(source.File.Groups))
	for groupIndex, group := range source.File.Groups {
		result.File.Groups[groupIndex] = group
		result.File.Groups[groupIndex].TranslationUnit = make([]XLIFFUnit, len(group.TranslationUnit))
		for unitIndex, unit := range group.TranslationUnit {
			unit.Target = nil
			unit.TargetInline = nil
			if target, ok := targets[unit.ID]; ok {
				translated := target.TranslatedText
				unit.Target = &translated
				unit.TargetInline = append([]XLIFFInline(nil), target.TargetInline...)
			}
			result.File.Groups[groupIndex].TranslationUnit[unitIndex] = unit
		}
	}
	return result
}

// MarshalXLIFF serializes the constrained document as standards-compliant
// XLIFF 2.2 XML. Template placeholders are emitted as non-copyable,
// non-deletable ph inline codes.
func MarshalXLIFF(document *XLIFFDocument) ([]byte, error) {
	if err := ValidateXLIFFDocument(document, false); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	output.WriteString(xml.Header)
	encoder := xml.NewEncoder(&output)

	xliff := xml.StartElement{Name: xml.Name{Local: "xliff"}, Attr: []xml.Attr{
		{Name: xml.Name{Local: "xmlns"}, Value: XLIFFNamespace},
		{Name: xml.Name{Local: "version"}, Value: document.Version},
		{Name: xml.Name{Local: "srcLang"}, Value: document.SourceLocale},
		{Name: xml.Name{Local: "trgLang"}, Value: document.TargetLocale},
	}}
	if err := encoder.EncodeToken(xliff); err != nil {
		return nil, err
	}
	file := xml.StartElement{Name: xml.Name{Local: "file"}, Attr: []xml.Attr{{Name: xml.Name{Local: "id"}, Value: document.File.ID}}}
	if err := encoder.EncodeToken(file); err != nil {
		return nil, err
	}
	for _, group := range document.File.Groups {
		if err := encodeXLIFFGroup(encoder, group); err != nil {
			return nil, err
		}
	}
	if err := encoder.EncodeToken(file.End()); err != nil {
		return nil, err
	}
	if err := encoder.EncodeToken(xliff.End()); err != nil {
		return nil, err
	}
	if err := encoder.Flush(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func encodeXLIFFGroup(encoder *xml.Encoder, group XLIFFGroup) error {
	start := xml.StartElement{Name: xml.Name{Local: "group"}, Attr: []xml.Attr{{Name: xml.Name{Local: "id"}, Value: group.ID}}}
	if err := encoder.EncodeToken(start); err != nil {
		return err
	}
	notes := normalizedXLIFFNotes(group)
	if len(notes) > 0 {
		if err := encodeXLIFFNotes(encoder, notes); err != nil {
			return err
		}
	}
	for _, unit := range group.TranslationUnit {
		if err := encodeXLIFFUnit(encoder, unit); err != nil {
			return err
		}
	}
	return encoder.EncodeToken(start.End())
}

func encodeXLIFFUnit(encoder *xml.Encoder, unit XLIFFUnit) error {
	normalized, err := normalizeXLIFFUnitInline(unit)
	if err != nil {
		return err
	}
	attributes := []xml.Attr{{Name: xml.Name{Local: "id"}, Value: unit.ID}, {Name: xml.Name{Local: "canResegment"}, Value: "no"}}
	if strings.TrimSpace(unit.Name) != "" {
		attributes = append(attributes, xml.Attr{Name: xml.Name{Local: "name"}, Value: unit.Name})
	}
	start := xml.StartElement{Name: xml.Name{Local: "unit"}, Attr: attributes}
	if err := encoder.EncodeToken(start); err != nil {
		return err
	}
	if unit.Context != nil && strings.TrimSpace(*unit.Context) != "" {
		if err := encodeXLIFFNotes(encoder, []xliffNote{{Category: "context", Value: strings.TrimSpace(*unit.Context)}}); err != nil {
			return err
		}
	}

	if len(normalized.OriginalData) > 0 {
		originalData := xml.StartElement{Name: xml.Name{Local: "originalData"}}
		if err := encoder.EncodeToken(originalData); err != nil {
			return err
		}
		for _, original := range normalized.OriginalData {
			data := xml.StartElement{Name: xml.Name{Local: "data"}, Attr: []xml.Attr{{Name: xml.Name{Local: "id"}, Value: original.ID}}}
			if err := encoder.EncodeElement(original.Value, data); err != nil {
				return err
			}
		}
		if err := encoder.EncodeToken(originalData.End()); err != nil {
			return err
		}
	}

	segment := xml.StartElement{Name: xml.Name{Local: "segment"}, Attr: []xml.Attr{{Name: xml.Name{Local: "id"}, Value: "s1"}}}
	if err := encoder.EncodeToken(segment); err != nil {
		return err
	}
	if err := encodeXLIFFInlineContent(encoder, "source", normalized.SourceInline); err != nil {
		return err
	}
	if unit.Target != nil {
		if err := encodeXLIFFInlineContent(encoder, "target", normalized.TargetInline); err != nil {
			return err
		}
	}
	if err := encoder.EncodeToken(segment.End()); err != nil {
		return err
	}
	return encoder.EncodeToken(start.End())
}

func encodeXLIFFInlineContent(encoder *xml.Encoder, name string, inline []XLIFFInline) error {
	start := xml.StartElement{Name: xml.Name{Local: name}, Attr: []xml.Attr{{
		Name: xml.Name{Local: "xml:space"}, Value: "preserve",
	}}}
	if err := encoder.EncodeToken(start); err != nil {
		return err
	}
	if err := encodeXLIFFInlineNodes(encoder, inline); err != nil {
		return err
	}
	return encoder.EncodeToken(start.End())
}

func encodeXLIFFInlineNodes(encoder *xml.Encoder, inline []XLIFFInline) error {
	for _, node := range inline {
		switch node.Kind {
		case XLIFFInlineText:
			if err := encoder.EncodeToken(xml.CharData(node.Text)); err != nil {
				return err
			}
		case XLIFFInlinePlaceholder:
			start := xml.StartElement{Name: xml.Name{Local: "ph"}, Attr: xliffInlineAttributes(node)}
			if err := encoder.EncodeToken(start); err != nil {
				return err
			}
			if err := encoder.EncodeToken(start.End()); err != nil {
				return err
			}
		case XLIFFInlinePairedCode:
			start := xml.StartElement{Name: xml.Name{Local: "pc"}, Attr: xliffInlineAttributes(node)}
			if err := encoder.EncodeToken(start); err != nil {
				return err
			}
			if err := encodeXLIFFInlineNodes(encoder, node.Children); err != nil {
				return err
			}
			if err := encoder.EncodeToken(start.End()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported XLIFF inline kind %q", node.Kind)
		}
	}
	return nil
}

// MarshalXLIFFInlineFragment serializes one provider-visible semantic segment
// as stable XLIFF pc/ph tokens without an enclosing source or target element.
func MarshalXLIFFInlineFragment(inline []XLIFFInline) (string, error) {
	var output bytes.Buffer
	encoder := xml.NewEncoder(&output)
	if err := encodeXLIFFInlineNodes(encoder, inline); err != nil {
		return "", err
	}
	if err := encoder.Flush(); err != nil {
		return "", err
	}
	return output.String(), nil
}

func xliffInlineAttributes(node XLIFFInline) []xml.Attr {
	attributes := []xml.Attr{{Name: xml.Name{Local: "id"}, Value: node.ID}}
	values := []struct {
		name  string
		value string
	}{
		{name: "dataRef", value: node.DataRef},
		{name: "dataRefStart", value: node.DataRefStart},
		{name: "dataRefEnd", value: node.DataRefEnd},
		{name: "canCopy", value: node.CanCopy},
		{name: "canDelete", value: node.CanDelete},
	}
	for _, value := range values {
		if value.value != "" {
			attributes = append(attributes, xml.Attr{Name: xml.Name{Local: value.name}, Value: value.value})
		}
	}
	return attributes
}

type xliffNote struct {
	Category string
	Value    string
}

func normalizedXLIFFNotes(group XLIFFGroup) []xliffNote {
	values := []xliffNote{
		{Category: "context-title", Value: pointerValue(group.ContextTitle)},
		{Category: "context-before", Value: pointerValue(group.ContextBefore)},
		{Category: "context-after", Value: pointerValue(group.ContextAfter)},
	}
	notes := make([]xliffNote, 0, len(values))
	for _, value := range values {
		value.Value = strings.TrimSpace(value.Value)
		if value.Value != "" {
			notes = append(notes, value)
		}
	}
	return notes
}

func encodeXLIFFNotes(encoder *xml.Encoder, notes []xliffNote) error {
	start := xml.StartElement{Name: xml.Name{Local: "notes"}}
	if err := encoder.EncodeToken(start); err != nil {
		return err
	}
	for index, note := range notes {
		noteStart := xml.StartElement{Name: xml.Name{Local: "note"}, Attr: []xml.Attr{
			{Name: xml.Name{Local: "id"}, Value: fmt.Sprintf("n%d", index+1)},
			{Name: xml.Name{Local: "category"}, Value: note.Category},
		}}
		if err := encoder.EncodeElement(note.Value, noteStart); err != nil {
			return err
		}
	}
	return encoder.EncodeToken(start.End())
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
