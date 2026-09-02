package email

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/net/html"

	"github.com/echovisionlab/geul-api/internal/translation"
)

const (
	layoutUnitMarkerPrefix  = "geul-unit:"
	layoutValueMarkerPrefix = "geul-value:"
	layoutOverlayMarker     = "geul-overlay:v1"
)

type layoutUnitMarkerKind string

const (
	layoutTextMarker    layoutUnitMarkerKind = "text"
	layoutElementMarker layoutUnitMarkerKind = "element"
)

// LayoutContentUnit is one stable, locale-owned Email Layout value. MarkerID
// identifies its source wrapper position without exposing positional HTML
// indexes to Translation or DCDP consumers.
type LayoutContentUnit struct {
	Handle       string
	MarkerID     uuid.UUID
	Kind         string
	Element      string
	Attribute    string
	Order        int
	SourceValue  string
	SourceFormat string
	Context      string
}

type layoutUnitMarker struct {
	Kind layoutUnitMarkerKind
	ID   uuid.UUID
}

type layoutValueMarker struct {
	Kind      layoutUnitMarkerKind
	ID        uuid.UUID
	Attribute string
}

// CanonicalizeLayoutSourceMarkers assigns an invisible durable UUID marker to
// every translatable source text node and every element with translatable
// attributes. Existing valid markers are preserved byte-for-byte by identity;
// markers whose source unit was removed are discarded.
func CanonicalizeLayoutSourceMarkers(content string) (string, error) {
	tokenizer := html.NewTokenizer(strings.NewReader(content))
	var output strings.Builder
	seen := make(map[uuid.UUID]struct{})
	var pending *layoutUnitMarker
	excludedDepth := 0
	flushEmptyText := func() error {
		if pending == nil {
			return nil
		}
		marker := pending
		pending = nil
		if marker.Kind != layoutTextMarker {
			return nil
		}
		canonical, err := canonicalLayoutMarker(marker, layoutTextMarker, true, seen)
		if err != nil {
			return err
		}
		output.WriteString(formatLayoutUnitMarker(*canonical))
		return nil
	}

	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case html.ErrorToken:
			if !errors.Is(tokenizer.Err(), io.EOF) {
				return "", fmt.Errorf("tokenize Email Layout source: %w", tokenizer.Err())
			}
			if err := flushEmptyText(); err != nil {
				return "", err
			}
			return output.String(), nil
		case html.CommentToken:
			data := tokenizer.Token().Data
			marker, recognized, err := parseLayoutUnitMarker(data)
			if err != nil {
				return "", err
			}
			if recognized {
				if err := flushEmptyText(); err != nil {
					return "", err
				}
				// A text marker without a following text token is a durable explicit
				// empty unit. Element markers whose attribute was removed are dropped.
				pending = &marker
				continue
			}
			if data == layoutOverlayMarker || strings.HasPrefix(data, layoutValueMarkerPrefix) {
				return "", errors.New("Email Layout source cannot contain target value markers")
			}
			if err := flushEmptyText(); err != nil {
				return "", err
			}
			output.Write(tokenizer.Raw())
		case html.StartTagToken, html.SelfClosingTagToken:
			if pending != nil && pending.Kind == layoutTextMarker {
				if err := flushEmptyText(); err != nil {
					return "", err
				}
			}
			token := tokenizer.Token()
			isExcluded := translation.IsExcludedHTMLElement(token.DataAtom)
			marked := pending != nil && pending.Kind == layoutElementMarker
			eligible := excludedDepth == 0 && !isExcluded && len(layoutTranslatableAttributesForMarker(token, marked)) != 0
			marker, err := canonicalLayoutMarker(pending, layoutElementMarker, eligible, seen)
			if err != nil {
				return "", err
			}
			pending = nil
			if marker != nil {
				output.WriteString(formatLayoutUnitMarker(*marker))
			}
			output.Write(tokenizer.Raw())
			if tokenType == html.StartTagToken && isExcluded {
				excludedDepth++
			}
		case html.EndTagToken:
			if err := flushEmptyText(); err != nil {
				return "", err
			}
			token := tokenizer.Token()
			if translation.IsExcludedHTMLElement(token.DataAtom) && excludedDepth > 0 {
				excludedDepth--
			}
			output.Write(tokenizer.Raw())
		case html.TextToken:
			text := string(tokenizer.Text())
			eligible := excludedDepth == 0 && layoutTextIsTranslatable(text)
			if !eligible {
				if err := flushEmptyText(); err != nil {
					return "", err
				}
			}
			marker, err := canonicalLayoutMarker(pending, layoutTextMarker, eligible, seen)
			if err != nil {
				return "", err
			}
			pending = nil
			if marker != nil {
				output.WriteString(formatLayoutUnitMarker(*marker))
			}
			output.Write(tokenizer.Raw())
		default:
			if err := flushEmptyText(); err != nil {
				return "", err
			}
			output.Write(tokenizer.Raw())
		}
	}
}

func canonicalLayoutMarker(
	pending *layoutUnitMarker,
	requiredKind layoutUnitMarkerKind,
	eligible bool,
	seen map[uuid.UUID]struct{},
) (*layoutUnitMarker, error) {
	if !eligible {
		return nil, nil
	}
	marker := layoutUnitMarker{Kind: requiredKind, ID: uuid.New()}
	if pending != nil && pending.Kind == requiredKind {
		marker = *pending
	}
	if _, duplicate := seen[marker.ID]; duplicate {
		return nil, fmt.Errorf("duplicate Email Layout unit marker %q", marker.ID)
	}
	seen[marker.ID] = struct{}{}
	return &marker, nil
}

// ExtractLayoutContentUnits returns the canonical source unit descriptor and
// source value sequence. Missing, detached, duplicated, or target-only markers
// fail closed instead of falling back to unstable positions.
func ExtractLayoutContentUnits(content string) ([]LayoutContentUnit, error) {
	tokenizer := html.NewTokenizer(strings.NewReader(content))
	units := make([]LayoutContentUnit, 0)
	seenMarkers := make(map[uuid.UUID]struct{})
	seenHandles := make(map[string]struct{})
	stack := make([]string, 0, 8)
	var pending *layoutUnitMarker
	excludedDepth := 0
	appendTextUnit := func(marker layoutUnitMarker, value string) error {
		handle := layoutTextUnitHandle(marker.ID)
		if _, duplicate := seenHandles[handle]; duplicate {
			return fmt.Errorf("duplicate Email Layout unit handle %q", handle)
		}
		seenHandles[handle] = struct{}{}
		context := ""
		if len(stack) != 0 {
			context = stack[len(stack)-1]
		}
		units = append(units, LayoutContentUnit{
			Handle: handle, MarkerID: marker.ID, Kind: "text", Order: len(units),
			SourceValue: value, SourceFormat: translation.SourceFormatHTMLText, Context: context,
		})
		return nil
	}
	flushEmptyText := func() error {
		if pending == nil {
			return nil
		}
		marker := *pending
		pending = nil
		if marker.Kind != layoutTextMarker {
			return errors.New("Email Layout element unit marker is detached from its source element")
		}
		return appendTextUnit(marker, "")
	}

	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case html.ErrorToken:
			if !errors.Is(tokenizer.Err(), io.EOF) {
				return nil, fmt.Errorf("tokenize Email Layout source: %w", tokenizer.Err())
			}
			if err := flushEmptyText(); err != nil {
				return nil, err
			}
			return units, nil
		case html.CommentToken:
			data := tokenizer.Token().Data
			marker, recognized, err := parseLayoutUnitMarker(data)
			if err != nil {
				return nil, err
			}
			if recognized {
				if err := flushEmptyText(); err != nil {
					return nil, err
				}
				if _, duplicate := seenMarkers[marker.ID]; duplicate {
					return nil, fmt.Errorf("duplicate Email Layout unit marker %q", marker.ID)
				}
				seenMarkers[marker.ID] = struct{}{}
				pending = &marker
				continue
			}
			if data == layoutOverlayMarker || strings.HasPrefix(data, layoutValueMarkerPrefix) {
				return nil, errors.New("Email Layout source contains a target value marker")
			}
			if err := flushEmptyText(); err != nil {
				return nil, err
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			if pending != nil && pending.Kind == layoutTextMarker {
				if err := flushEmptyText(); err != nil {
					return nil, err
				}
			}
			token := tokenizer.Token()
			isExcluded := translation.IsExcludedHTMLElement(token.DataAtom)
			marked := pending != nil && pending.Kind == layoutElementMarker
			attributes := layoutTranslatableAttributesForMarker(token, marked)
			eligible := excludedDepth == 0 && !isExcluded && len(attributes) != 0
			if eligible {
				if pending == nil || pending.Kind != layoutElementMarker {
					return nil, fmt.Errorf("Email Layout element <%s> is missing its durable unit marker", token.Data)
				}
				for _, attribute := range attributes {
					handle := layoutAttributeUnitHandle(pending.ID, attribute.Name)
					if _, duplicate := seenHandles[handle]; duplicate {
						return nil, fmt.Errorf("duplicate Email Layout unit handle %q", handle)
					}
					seenHandles[handle] = struct{}{}
					units = append(units, LayoutContentUnit{
						Handle: handle, MarkerID: pending.ID, Kind: "attribute",
						Element: token.Data, Attribute: attribute.Name, Order: len(units),
						SourceValue: attribute.Value, SourceFormat: translation.SourceFormatPlainText,
						Context: token.Data + "[" + attribute.Name + "]",
					})
				}
			} else if pending != nil {
				return nil, errors.New("Email Layout element unit marker has no translatable attributes")
			}
			pending = nil
			if tokenType == html.StartTagToken {
				stack = append(stack, token.Data)
				if isExcluded {
					excludedDepth++
				}
			}
		case html.EndTagToken:
			if err := flushEmptyText(); err != nil {
				return nil, err
			}
			token := tokenizer.Token()
			if translation.IsExcludedHTMLElement(token.DataAtom) && excludedDepth > 0 {
				excludedDepth--
			}
			stack = popLayoutHTMLElementStack(stack, token.Data)
		case html.TextToken:
			text := string(tokenizer.Text())
			eligible := excludedDepth == 0 && layoutTextIsTranslatable(text)
			if eligible {
				if pending == nil || pending.Kind != layoutTextMarker {
					return nil, errors.New("Email Layout text is missing its durable unit marker")
				}
				if err := appendTextUnit(*pending, text); err != nil {
					return nil, err
				}
			} else if err := flushEmptyText(); err != nil {
				return nil, err
			}
			pending = nil
		default:
			if err := flushEmptyText(); err != nil {
				return nil, err
			}
		}
	}
}

type layoutAttributeValue struct {
	Name  string
	Value string
}

func layoutTranslatableAttributesForMarker(token html.Token, preserveEmpty bool) []layoutAttributeValue {
	values := make([]layoutAttributeValue, 0, len(token.Attr))
	for _, attribute := range token.Attr {
		name := strings.ToLower(strings.TrimSpace(attribute.Key))
		if _, allowed := layoutHTMLTranslatableAttributes[name]; !allowed {
			continue
		}
		value := strings.TrimSpace(attribute.Val)
		if (!preserveEmpty && value == "") || translation.IsPurePlaceholderText(value) {
			continue
		}
		values = append(values, layoutAttributeValue{Name: name, Value: attribute.Val})
	}
	return values
}

func layoutTextIsTranslatable(text string) bool {
	trimmed := strings.TrimSpace(text)
	return trimmed != "" && !translation.IsPurePlaceholderText(trimmed)
}

// ApplyLayoutLocaleValues stores a sparse locale overlay on a canonical source
// wrapper. Every explicit value receives an invisible value marker; omission is
// therefore distinct from an explicit empty string without another DB column.
func ApplyLayoutLocaleValues(
	source string,
	values map[string]string,
) (*string, *string, error) {
	units, err := ExtractLayoutContentUnits(source)
	if err != nil {
		return nil, nil, err
	}
	// Unknown handles are intentionally ignored below. Translation job
	// reconciliation applies only the current source graph intersection.

	tokenizer := html.NewTokenizer(strings.NewReader(source))
	var output strings.Builder
	output.WriteString("<!--" + layoutOverlayMarker + "-->")
	textParts := make([]string, 0, len(units))
	var pending *layoutUnitMarker
	flushEmptyText := func() {
		if pending == nil || pending.Kind != layoutTextMarker {
			pending = nil
			return
		}
		handle := layoutTextUnitHandle(pending.ID)
		if value, explicit := values[handle]; explicit {
			output.WriteString(formatLayoutValueMarker(layoutValueMarker{Kind: layoutTextMarker, ID: pending.ID}))
			text := layoutTranslatedValue("", value)
			output.WriteString(html.EscapeString(text))
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				textParts = append(textParts, trimmed)
			}
		}
		pending = nil
	}

	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case html.ErrorToken:
			if !errors.Is(tokenizer.Err(), io.EOF) {
				return nil, nil, fmt.Errorf("tokenize canonical Email Layout source: %w", tokenizer.Err())
			}
			flushEmptyText()
			rendered := output.String()
			text := strings.Join(textParts, "\n")
			return stringPointer(rendered), stringPointer(text), nil
		case html.CommentToken:
			marker, recognized, parseErr := parseLayoutUnitMarker(tokenizer.Token().Data)
			if parseErr != nil {
				return nil, nil, parseErr
			}
			if recognized {
				flushEmptyText()
				pending = &marker
				output.WriteString(formatLayoutUnitMarker(marker))
				continue
			}
			flushEmptyText()
			output.Write(tokenizer.Raw())
		case html.StartTagToken, html.SelfClosingTagToken:
			if pending != nil && pending.Kind == layoutTextMarker {
				flushEmptyText()
			}
			token := tokenizer.Token()
			changed := false
			if pending != nil && pending.Kind == layoutElementMarker {
				for index := range token.Attr {
					attribute := &token.Attr[index]
					name := strings.ToLower(strings.TrimSpace(attribute.Key))
					value, explicit := values[layoutAttributeUnitHandle(pending.ID, name)]
					if !explicit {
						continue
					}
					output.WriteString(formatLayoutValueMarker(layoutValueMarker{
						Kind: layoutElementMarker, ID: pending.ID, Attribute: name,
					}))
					attribute.Val = layoutTranslatedValue(attribute.Val, value)
					changed = true
				}
			}
			pending = nil
			if changed {
				output.WriteString(token.String())
			} else {
				output.Write(tokenizer.Raw())
			}
		case html.TextToken:
			text := string(tokenizer.Text())
			if pending != nil && pending.Kind == layoutTextMarker {
				handle := layoutTextUnitHandle(pending.ID)
				if value, explicit := values[handle]; explicit {
					output.WriteString(formatLayoutValueMarker(layoutValueMarker{Kind: layoutTextMarker, ID: pending.ID}))
					text = layoutTranslatedValue(text, value)
					output.WriteString(html.EscapeString(text))
				} else {
					output.Write(tokenizer.Raw())
				}
				if trimmed := strings.TrimSpace(text); trimmed != "" {
					textParts = append(textParts, trimmed)
				}
				pending = nil
				continue
			}
			pending = nil
			output.Write(tokenizer.Raw())
		default:
			flushEmptyText()
			output.Write(tokenizer.Raw())
		}
	}
}

// ApplyLayoutSourceValues changes canonical source values while retaining the
// durable structural markers, including marker-only explicit-empty text units.
func ApplyLayoutSourceValues(source string, values map[string]string) (*string, *string, error) {
	encoded, text, err := ApplyLayoutLocaleValues(source, values)
	if err != nil {
		return nil, nil, err
	}
	withoutValues, err := stripLayoutValueMarkers(dereferenceString(encoded))
	if err != nil {
		return nil, nil, err
	}
	canonical, err := CanonicalizeLayoutSourceMarkers(withoutValues)
	if err != nil {
		return nil, nil, err
	}
	return stringPointer(canonical), text, nil
}

// ExtractLayoutLocaleValues reads only values carrying an explicit target
// marker. Visible source fallback copied into the stored wrapper remains absent.
func ExtractLayoutLocaleValues(content string) (map[string]string, error) {
	tokenizer := html.NewTokenizer(strings.NewReader(content))
	values := make(map[string]string)
	var unit *layoutUnitMarker
	valueMarkers := make([]layoutValueMarker, 0, 3)
	overlayFound := false

	flushEmptyText := func() error {
		if unit == nil || unit.Kind != layoutTextMarker {
			return nil
		}
		for _, marker := range valueMarkers {
			if marker.Kind != layoutTextMarker || marker.ID != unit.ID {
				return errors.New("Email Layout target value marker does not match its text unit")
			}
			handle := layoutTextUnitHandle(unit.ID)
			if _, duplicate := values[handle]; duplicate {
				return fmt.Errorf("duplicate Email Layout target value %q", handle)
			}
			values[handle] = ""
		}
		return nil
	}

	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case html.ErrorToken:
			if !errors.Is(tokenizer.Err(), io.EOF) {
				return nil, fmt.Errorf("tokenize Email Layout target: %w", tokenizer.Err())
			}
			if err := flushEmptyText(); err != nil {
				return nil, err
			}
			if !overlayFound {
				return nil, errors.New("Email Layout target sparse-overlay marker is missing")
			}
			return values, nil
		case html.CommentToken:
			data := tokenizer.Token().Data
			if data == layoutOverlayMarker {
				if overlayFound || unit != nil || len(valueMarkers) != 0 {
					return nil, errors.New("Email Layout target sparse-overlay marker is duplicated or misplaced")
				}
				overlayFound = true
				continue
			}
			marker, recognized, err := parseLayoutUnitMarker(data)
			if err != nil {
				return nil, err
			}
			if recognized {
				if err := flushEmptyText(); err != nil {
					return nil, err
				}
				unit = &marker
				valueMarkers = valueMarkers[:0]
				continue
			}
			valueMarker, recognized, err := parseLayoutValueMarker(data)
			if err != nil {
				return nil, err
			}
			if recognized {
				if unit == nil || unit.ID != valueMarker.ID || unit.Kind != valueMarker.Kind {
					return nil, errors.New("Email Layout target value marker is detached from its unit")
				}
				valueMarkers = append(valueMarkers, valueMarker)
				continue
			}
			if len(valueMarkers) != 0 {
				return nil, errors.New("Email Layout target value marker is detached from its value")
			}
		case html.TextToken:
			if unit != nil && unit.Kind == layoutTextMarker {
				for _, marker := range valueMarkers {
					if marker.Kind != layoutTextMarker || marker.ID != unit.ID {
						return nil, errors.New("Email Layout target text marker mismatch")
					}
					handle := layoutTextUnitHandle(unit.ID)
					if _, duplicate := values[handle]; duplicate {
						return nil, fmt.Errorf("duplicate Email Layout target value %q", handle)
					}
					values[handle] = string(tokenizer.Text())
				}
			}
			unit = nil
			valueMarkers = valueMarkers[:0]
		case html.StartTagToken, html.SelfClosingTagToken:
			if unit != nil && unit.Kind == layoutElementMarker {
				token := tokenizer.Token()
				attributes := make(map[string]string, len(token.Attr))
				for _, attribute := range token.Attr {
					attributes[strings.ToLower(strings.TrimSpace(attribute.Key))] = attribute.Val
				}
				for _, marker := range valueMarkers {
					value, exists := attributes[marker.Attribute]
					if !exists {
						return nil, fmt.Errorf("Email Layout target attribute %q is missing", marker.Attribute)
					}
					handle := layoutAttributeUnitHandle(unit.ID, marker.Attribute)
					if _, duplicate := values[handle]; duplicate {
						return nil, fmt.Errorf("duplicate Email Layout target value %q", handle)
					}
					values[handle] = value
				}
			} else if len(valueMarkers) != 0 {
				return nil, errors.New("Email Layout target attribute marker is detached from its element")
			}
			unit = nil
			valueMarkers = valueMarkers[:0]
		default:
			if err := flushEmptyText(); err != nil {
				return nil, err
			}
			unit = nil
			valueMarkers = valueMarkers[:0]
		}
	}
}

// ResolveLayoutLocaleMarkup merges explicit target values onto the current
// source structure and removes every private marker before rendering.
func ResolveLayoutLocaleMarkup(source string, target *string) (string, error) {
	if target == nil {
		return StripLayoutPrivateMarkers(source)
	}
	values, err := ExtractLayoutStoredLocaleValues(*target)
	if err != nil {
		return "", err
	}
	encoded, _, err := ApplyLayoutLocaleValues(source, values)
	if err != nil {
		return "", err
	}
	return StripLayoutPrivateMarkers(dereferenceString(encoded))
}

// StripLayoutPrivateMarkers removes unit and value comments without
// serializing ordinary tags, preserving the exact rendered wrapper bytes.
func StripLayoutPrivateMarkers(content string) (string, error) {
	tokenizer := html.NewTokenizer(strings.NewReader(content))
	var output strings.Builder
	for {
		tokenType := tokenizer.Next()
		if tokenType == html.ErrorToken {
			if errors.Is(tokenizer.Err(), io.EOF) {
				return output.String(), nil
			}
			return "", fmt.Errorf("tokenize Email Layout render wrapper: %w", tokenizer.Err())
		}
		if tokenType == html.CommentToken {
			data := tokenizer.Token().Data
			if data == layoutOverlayMarker {
				continue
			}
			_, unit, err := parseLayoutUnitMarker(data)
			if err != nil {
				return "", err
			}
			_, value, err := parseLayoutValueMarker(data)
			if err != nil {
				return "", err
			}
			if unit || value {
				continue
			}
		}
		output.Write(tokenizer.Raw())
	}
}

func stripLayoutValueMarkers(content string) (string, error) {
	tokenizer := html.NewTokenizer(strings.NewReader(content))
	var output strings.Builder
	for {
		tokenType := tokenizer.Next()
		if tokenType == html.ErrorToken {
			if errors.Is(tokenizer.Err(), io.EOF) {
				return output.String(), nil
			}
			return "", fmt.Errorf("tokenize Email Layout source value wrapper: %w", tokenizer.Err())
		}
		if tokenType == html.CommentToken {
			data := tokenizer.Token().Data
			if data == layoutOverlayMarker {
				continue
			}
			_, value, err := parseLayoutValueMarker(data)
			if err != nil {
				return "", err
			}
			if value {
				continue
			}
		}
		output.Write(tokenizer.Raw())
	}
}

func parseLayoutUnitMarker(data string) (layoutUnitMarker, bool, error) {
	if !strings.HasPrefix(data, layoutUnitMarkerPrefix) {
		return layoutUnitMarker{}, false, nil
	}
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != "geul-unit" {
		return layoutUnitMarker{}, false, errors.New("malformed Email Layout unit marker")
	}
	kind := layoutUnitMarkerKind(parts[1])
	if kind != layoutTextMarker && kind != layoutElementMarker {
		return layoutUnitMarker{}, false, fmt.Errorf("unsupported Email Layout unit marker kind %q", kind)
	}
	id, err := uuid.Parse(parts[2])
	if err != nil || id == uuid.Nil || id.String() != parts[2] {
		return layoutUnitMarker{}, false, errors.New("Email Layout unit marker must contain a canonical UUID")
	}
	return layoutUnitMarker{Kind: kind, ID: id}, true, nil
}

func parseLayoutValueMarker(data string) (layoutValueMarker, bool, error) {
	if !strings.HasPrefix(data, layoutValueMarkerPrefix) {
		return layoutValueMarker{}, false, nil
	}
	parts := strings.Split(data, ":")
	if len(parts) < 3 || parts[0] != "geul-value" {
		return layoutValueMarker{}, false, errors.New("malformed Email Layout value marker")
	}
	kind := layoutUnitMarkerKind(parts[1])
	id, err := uuid.Parse(parts[2])
	if err != nil || id == uuid.Nil || id.String() != parts[2] {
		return layoutValueMarker{}, false, errors.New("Email Layout value marker must contain a canonical UUID")
	}
	switch kind {
	case layoutTextMarker:
		if len(parts) != 3 {
			return layoutValueMarker{}, false, errors.New("malformed Email Layout text value marker")
		}
		return layoutValueMarker{Kind: kind, ID: id}, true, nil
	case layoutElementMarker:
		if len(parts) != 4 {
			return layoutValueMarker{}, false, errors.New("malformed Email Layout attribute value marker")
		}
		attribute := strings.ToLower(strings.TrimSpace(parts[3]))
		if _, allowed := layoutHTMLTranslatableAttributes[attribute]; !allowed {
			return layoutValueMarker{}, false, fmt.Errorf("unsupported Email Layout target attribute %q", attribute)
		}
		return layoutValueMarker{Kind: kind, ID: id, Attribute: attribute}, true, nil
	default:
		return layoutValueMarker{}, false, fmt.Errorf("unsupported Email Layout value marker kind %q", kind)
	}
}

func formatLayoutUnitMarker(marker layoutUnitMarker) string {
	return "<!--" + layoutUnitMarkerPrefix + string(marker.Kind) + ":" + marker.ID.String() + "-->"
}

func formatLayoutValueMarker(marker layoutValueMarker) string {
	value := "<!--" + layoutValueMarkerPrefix + string(marker.Kind) + ":" + marker.ID.String()
	if marker.Attribute != "" {
		value += ":" + marker.Attribute
	}
	return value + "-->"
}

func layoutTextUnitHandle(id uuid.UUID) string {
	return "unit:" + id.String() + ":text"
}

func layoutAttributeUnitHandle(id uuid.UUID, attribute string) string {
	return "unit:" + id.String() + ":attr:" + strings.ToLower(strings.TrimSpace(attribute))
}

func layoutTranslatedValue(source, value string) string {
	if value == "" {
		return ""
	}
	return translation.PreserveSourceEdgeWhitespace(source, value)
}
