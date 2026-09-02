package translation

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"reflect"
	"strings"
)

type xliffXMLDocument struct {
	XMLName    xml.Name       `xml:"xliff"`
	Version    string         `xml:"version,attr"`
	SourceLang string         `xml:"srcLang,attr"`
	TargetLang string         `xml:"trgLang,attr"`
	Files      []xliffXMLFile `xml:"file"`
}

type xliffXMLFile struct {
	ID     string          `xml:"id,attr"`
	Groups []xliffXMLGroup `xml:"group"`
}

type xliffXMLGroup struct {
	ID    string         `xml:"id,attr"`
	Notes []xliffXMLNote `xml:"notes>note"`
	Units []xliffXMLUnit `xml:"unit"`
}

type xliffXMLNote struct {
	Category string `xml:"category,attr"`
	Value    string `xml:",chardata"`
}

type xliffXMLUnit struct {
	ID           string                 `xml:"id,attr"`
	Name         string                 `xml:"name,attr"`
	OriginalData []xliffXMLOriginalData `xml:"originalData>data"`
	Segments     []xliffXMLSegment      `xml:"segment"`
	Notes        []xliffXMLNote         `xml:"notes>note"`
}

type xliffXMLOriginalData struct {
	ID    string `xml:"id,attr"`
	Value string `xml:",chardata"`
}

type xliffXMLSegment struct {
	ID     string           `xml:"id,attr"`
	Source xliffXMLContent  `xml:"source"`
	Target *xliffXMLContent `xml:"target"`
}

type xliffXMLContent struct {
	InnerXML string `xml:",innerxml"`
}

// UnmarshalXLIFF parses the constrained Geul XLIFF 2.2 profile without
// flattening inline codes. Provider-specific relative validation belongs in
// ParseTranslatedXLIFF, because an arbitrary document cannot establish source
// identity by itself.
func UnmarshalXLIFF(body []byte) (*XLIFFDocument, error) {
	return unmarshalXLIFF(body, ValidateXLIFFDocument)
}

// UnmarshalXLIFFInterchange parses the constrained CAT profile. Unlike a
// provider response, an explicit empty target is a valid locale value.
func UnmarshalXLIFFInterchange(body []byte) (*XLIFFDocument, error) {
	if err := validateXLIFFInterchangeXMLProfile(body); err != nil {
		return nil, err
	}
	return unmarshalXLIFF(body, func(document *XLIFFDocument, _ bool) error {
		return ValidateXLIFFInterchangeDocument(document)
	})
}

func unmarshalXLIFF(body []byte, validate func(*XLIFFDocument, bool) error) (*XLIFFDocument, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	decoder.Strict = true
	var encoded xliffXMLDocument
	if err := decoder.Decode(&encoded); err != nil {
		return nil, fmt.Errorf("failed to decode XLIFF document: %w", err)
	}
	if encoded.XMLName.Space != XLIFFNamespace {
		return nil, fmt.Errorf("unsupported XLIFF namespace %q", encoded.XMLName.Space)
	}
	if len(encoded.Files) != 1 {
		return nil, fmt.Errorf("XLIFF document must contain exactly one file")
	}

	document := &XLIFFDocument{
		Version: encoded.Version, SourceLocale: encoded.SourceLang, TargetLocale: encoded.TargetLang,
		File: XLIFFFile{ID: encoded.Files[0].ID, Groups: make([]XLIFFGroup, 0, len(encoded.Files[0].Groups))},
	}
	for _, encodedGroup := range encoded.Files[0].Groups {
		group := XLIFFGroup{ID: encodedGroup.ID, TranslationUnit: make([]XLIFFUnit, 0, len(encodedGroup.Units))}
		for _, note := range encodedGroup.Notes {
			value := strings.TrimSpace(note.Value)
			switch note.Category {
			case "context-title":
				group.ContextTitle = xliffStringPointer(value)
			case "context-before":
				group.ContextBefore = xliffStringPointer(value)
			case "context-after":
				group.ContextAfter = xliffStringPointer(value)
			}
		}
		for _, encodedUnit := range encodedGroup.Units {
			unit, err := decodeXLIFFUnit(encodedUnit)
			if err != nil {
				return nil, fmt.Errorf("XLIFF unit %q: %w", encodedUnit.ID, err)
			}
			group.TranslationUnit = append(group.TranslationUnit, unit)
		}
		document.File.Groups = append(document.File.Groups, group)
	}
	if err := validate(document, false); err != nil {
		return nil, err
	}
	return document, nil
}

var xliffInterchangeElements = map[string]map[string]struct{}{
	"xliff":        {"version": {}, "srcLang": {}, "trgLang": {}},
	"file":         {"id": {}},
	"group":        {"id": {}},
	"notes":        {},
	"note":         {"category": {}},
	"unit":         {"id": {}, "name": {}},
	"originalData": {},
	"data":         {"id": {}},
	"segment":      {"id": {}, "state": {}, "subState": {}},
	"source":       {"space": {}},
	"target":       {"space": {}},
	"ph":           {"id": {}, "dataRef": {}, "canCopy": {}, "canDelete": {}},
	"pc":           {"id": {}, "dataRefStart": {}, "dataRefEnd": {}, "canCopy": {}, "canDelete": {}},
}

func validateXLIFFInterchangeXMLProfile(body []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	decoder.Strict = true
	for {
		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("failed to decode XLIFF document: %w", err)
		}
		switch value := token.(type) {
		case xml.Directive:
			return fmt.Errorf("XLIFF directives and external entities are not supported")
		case xml.ProcInst:
			if value.Target != "xml" {
				return fmt.Errorf("XLIFF processing instruction %q is not supported", value.Target)
			}
		case xml.StartElement:
			allowedAttributes, ok := xliffInterchangeElements[value.Name.Local]
			if !ok || value.Name.Space != XLIFFNamespace {
				return fmt.Errorf("unsupported XLIFF element %q", value.Name.Local)
			}
			for _, attribute := range value.Attr {
				if strings.EqualFold(attribute.Name.Local, "href") {
					return fmt.Errorf("XLIFF remote href is not supported")
				}
				if attribute.Name.Space == "xmlns" || attribute.Name.Local == "xmlns" {
					continue
				}
				if attribute.Name.Space != "" && attribute.Name.Space != "http://www.w3.org/XML/1998/namespace" {
					return fmt.Errorf("unsupported XLIFF attribute %q", attribute.Name.Local)
				}
				if _, ok := allowedAttributes[attribute.Name.Local]; !ok {
					return fmt.Errorf("unsupported XLIFF attribute %q on %q", attribute.Name.Local, value.Name.Local)
				}
			}
		}
	}
}

func decodeXLIFFUnit(encoded xliffXMLUnit) (XLIFFUnit, error) {
	if len(encoded.Segments) != 1 || encoded.Segments[0].ID != "s1" {
		return XLIFFUnit{}, fmt.Errorf("exactly one segment with id s1 is required")
	}
	unit := XLIFFUnit{ID: encoded.ID, Name: encoded.Name}
	unit.OriginalData = make([]XLIFFOriginalData, 0, len(encoded.OriginalData))
	data := make(map[string]string, len(encoded.OriginalData))
	for _, original := range encoded.OriginalData {
		unit.OriginalData = append(unit.OriginalData, XLIFFOriginalData(original))
		data[original.ID] = original.Value
	}
	for _, note := range encoded.Notes {
		if note.Category == "context" {
			unit.Context = xliffStringPointer(strings.TrimSpace(note.Value))
		}
	}

	var err error
	unit.SourceInline, err = decodeXLIFFInlineContent(encoded.Segments[0].Source.InnerXML)
	if err != nil {
		return XLIFFUnit{}, fmt.Errorf("invalid source inline content: %w", err)
	}
	unit.Source, _, err = projectAndValidateXLIFFInline(unit.SourceInline, data, "")
	if err != nil {
		return XLIFFUnit{}, fmt.Errorf("invalid source inline content: %w", err)
	}
	if encoded.Segments[0].Target != nil {
		unit.TargetInline, err = decodeXLIFFInlineContent(encoded.Segments[0].Target.InnerXML)
		if err != nil {
			return XLIFFUnit{}, fmt.Errorf("invalid target inline content: %w", err)
		}
		target, _, projectErr := projectAndValidateXLIFFInline(unit.TargetInline, data, "")
		if projectErr != nil {
			return XLIFFUnit{}, fmt.Errorf("invalid target inline content: %w", projectErr)
		}
		unit.Target = &target
	}
	return unit, nil
}

func decodeXLIFFInlineContent(innerXML string) ([]XLIFFInline, error) {
	decoder := xml.NewDecoder(strings.NewReader("<content>" + innerXML + "</content>"))
	var stack [][]XLIFFInline
	stack = append(stack, nil)
	var paired []XLIFFInline
	for {
		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "content":
			case "ph":
				node := XLIFFInline{Kind: XLIFFInlinePlaceholder}
				setXLIFFInlineAttributes(&node, value.Attr)
				stack[len(stack)-1] = append(stack[len(stack)-1], node)
			case "pc":
				node := XLIFFInline{Kind: XLIFFInlinePairedCode}
				setXLIFFInlineAttributes(&node, value.Attr)
				paired = append(paired, node)
				stack = append(stack, nil)
			default:
				return nil, fmt.Errorf("unsupported XLIFF inline element %q", value.Name.Local)
			}
		case xml.EndElement:
			if value.Name.Local == "pc" {
				if len(paired) == 0 || len(stack) < 2 {
					return nil, fmt.Errorf("unbalanced paired inline code")
				}
				node := paired[len(paired)-1]
				paired = paired[:len(paired)-1]
				node.Children = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				stack[len(stack)-1] = append(stack[len(stack)-1], node)
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1] = appendXLIFFText(stack[len(stack)-1], string(value))
			}
		}
	}
	if len(stack) != 1 || len(paired) != 0 {
		return nil, fmt.Errorf("unbalanced paired inline code")
	}
	return stack[0], nil
}

// UnmarshalXLIFFInlineFragment parses the exact pc/ph token stream returned by
// a provider. Callers must validate it against the source unit before apply.
func UnmarshalXLIFFInlineFragment(fragment string) ([]XLIFFInline, error) {
	return decodeXLIFFInlineContent(fragment)
}

func setXLIFFInlineAttributes(node *XLIFFInline, attributes []xml.Attr) {
	for _, attribute := range attributes {
		switch attribute.Name.Local {
		case "id":
			node.ID = attribute.Value
		case "dataRef":
			node.DataRef = attribute.Value
		case "dataRefStart":
			node.DataRefStart = attribute.Value
		case "dataRefEnd":
			node.DataRefEnd = attribute.Value
		case "canCopy":
			node.CanCopy = attribute.Value
		case "canDelete":
			node.CanDelete = attribute.Value
		}
	}
}

// ParseTranslatedXLIFF parses provider output relative to the exact uploaded
// source and rejects locale, file, group, unit, source, original-data, or
// source-inline drift before exposing any translated targets.
func ParseTranslatedXLIFF(source XLIFFDocument, body []byte) (*ProviderResponse, error) {
	translated, err := UnmarshalXLIFF(body)
	if err != nil {
		return nil, err
	}
	if err := validateTranslatedXLIFFIdentity(source, *translated); err != nil {
		return nil, err
	}

	result := source
	result.File.Groups = make([]XLIFFGroup, len(source.File.Groups))
	for groupIndex, sourceGroup := range source.File.Groups {
		resultGroup := sourceGroup
		resultGroup.TranslationUnit = make([]XLIFFUnit, len(sourceGroup.TranslationUnit))
		for unitIndex, sourceUnit := range sourceGroup.TranslationUnit {
			translatedUnit := translated.File.Groups[groupIndex].TranslationUnit[unitIndex]
			resultUnit := sourceUnit
			resultUnit.Target = translatedUnit.Target
			resultUnit.TargetInline = translatedUnit.TargetInline
			if len(resultUnit.OriginalData) == 0 {
				resultUnit.OriginalData = translatedUnit.OriginalData
			}
			if len(resultUnit.SourceInline) == 0 {
				resultUnit.SourceInline = translatedUnit.SourceInline
			}
			resultGroup.TranslationUnit[unitIndex] = resultUnit
		}
		result.File.Groups[groupIndex] = resultGroup
	}
	if err := ValidateXLIFFDocument(&result, true); err != nil {
		return nil, err
	}
	return &ProviderResponse{Document: result}, nil
}

func validateTranslatedXLIFFIdentity(source XLIFFDocument, translated XLIFFDocument) error {
	if source.Version != translated.Version || source.SourceLocale != translated.SourceLocale || source.TargetLocale != translated.TargetLocale {
		return fmt.Errorf("translated XLIFF document identity changed")
	}
	if source.File.ID != translated.File.ID || len(source.File.Groups) != len(translated.File.Groups) {
		return fmt.Errorf("translated XLIFF file identity changed")
	}
	for groupIndex, sourceGroup := range source.File.Groups {
		translatedGroup := translated.File.Groups[groupIndex]
		if sourceGroup.ID != translatedGroup.ID || len(sourceGroup.TranslationUnit) != len(translatedGroup.TranslationUnit) {
			return fmt.Errorf("translated XLIFF group identity changed")
		}
		for unitIndex, sourceUnit := range sourceGroup.TranslationUnit {
			translatedUnit := translatedGroup.TranslationUnit[unitIndex]
			normalizedSource, err := normalizeXLIFFUnitInline(sourceUnit)
			if err != nil {
				return err
			}
			if sourceUnit.ID != translatedUnit.ID || sourceUnit.Name != translatedUnit.Name {
				return fmt.Errorf("translated XLIFF unit identity changed")
			}
			if normalizedSource.Source != translatedUnit.Source ||
				!reflect.DeepEqual(normalizedSource.OriginalData, translatedUnit.OriginalData) ||
				!reflect.DeepEqual(normalizedSource.SourceInline, translatedUnit.SourceInline) {
				return fmt.Errorf("translated XLIFF unit source changed")
			}
		}
	}
	return nil
}
