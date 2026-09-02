package aidocument

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Projection is the canonical compact read result. A field absent from a node
// is missing; a present text field whose value is "" is explicitly empty.
type Projection struct {
	Protocol         string            `json:"v"`
	Profile          Domain            `json:"p"`
	Catalog          string            `json:"c"`
	Document         DocumentReference `json:"d"`
	DocumentRevision Revision          `json:"dr"`
	TargetRevision   *Revision         `json:"tr,omitempty"`
	SourceLocale     Locale            `json:"s"`
	Locale           Locale            `json:"l"`
	LocaleRole       LocaleRole        `json:"lr"`
	LocaleExists     bool              `json:"le"`
	Mode             ReadMode          `json:"m"`
	Nodes            []Node            `json:"n"`
	Next             *Cursor           `json:"next"`
}

func EncodeOpenMetadata(metadata OpenMetadata) ([]byte, error) {
	if err := metadata.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(metadata)
}

func (p Projection) Identity() DocumentIdentity {
	return DocumentIdentity{Domain: p.Profile, Reference: p.Document}
}

func (p Projection) validate() error {
	if p.Protocol != ProtocolVersion {
		return fmt.Errorf("unsupported protocol %q", p.Protocol)
	}
	if err := p.Identity().validate(); err != nil {
		return err
	}
	if err := validateOpaque("catalog fingerprint", p.Catalog, 256); err != nil {
		return err
	}
	if err := validateOpaque("document revision", string(p.DocumentRevision), 256); err != nil {
		return err
	}
	if p.TargetRevision != nil {
		if err := validateOpaque("target revision", string(*p.TargetRevision), 256); err != nil {
			return err
		}
	}
	if err := validateLocale(p.SourceLocale); err != nil {
		return err
	}
	if err := validateLocale(p.Locale); err != nil {
		return err
	}
	if p.LocaleRole != deriveLocaleRole(p.SourceLocale, p.Locale) {
		return errors.New("locale role does not match source and requested locales")
	}
	if p.LocaleRole == LocaleRoleSource && !p.LocaleExists {
		return errors.New("source locale must exist")
	}
	if p.LocaleRole == LocaleRoleSource && p.TargetRevision != nil {
		return errors.New("source locale cannot carry a target revision")
	}
	if p.LocaleRole == LocaleRoleNonSource && p.LocaleExists && p.TargetRevision == nil {
		return errors.New("existing target locale requires a target revision")
	}
	if p.LocaleRole == LocaleRoleNonSource && !p.LocaleExists && p.TargetRevision != nil {
		return errors.New("absent target locale cannot carry a target revision")
	}
	if p.LocaleRole == LocaleRoleNonSource && !p.LocaleExists {
		for _, node := range p.Nodes {
			if len(node.Localized) != 0 {
				return errors.New("absent translation locale cannot contain locale-owned values")
			}
			for _, relation := range node.Relations {
				for _, item := range relation.Items {
					if len(item.Localized) != 0 {
						return errors.New("absent translation locale cannot contain relation-item locale-owned values")
					}
				}
			}
		}
	}
	if !p.Mode.valid() {
		return fmt.Errorf("unsupported read mode %q", p.Mode)
	}
	return validateProjectedNodes(p.Nodes)
}

func EncodeProjection(projection Projection) ([]byte, error) {
	if err := projection.validate(); err != nil {
		return nil, err
	}
	projection.Nodes = canonicalNodes(projection.Nodes)
	return json.Marshal(projection)
}

func EncodeApplyRequest(request ApplyRequest) ([]byte, error) {
	if err := request.validateEnvelope(); err != nil {
		return nil, err
	}
	if err := validateCanonicalOperations(request.Operations); err != nil {
		return nil, err
	}
	return json.Marshal(request)
}

func DecodeApplyRequest(data []byte) (ApplyRequest, error) {
	var request ApplyRequest
	if err := decodeStrict(data, &request); err != nil {
		return ApplyRequest{}, err
	}
	if err := request.validateEnvelope(); err != nil {
		return ApplyRequest{}, err
	}
	if err := validateCanonicalOperations(request.Operations); err != nil {
		return ApplyRequest{}, err
	}
	return request, nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func (v Value) MarshalJSON() ([]byte, error) {
	if err := v.validate(); err != nil {
		return nil, err
	}
	if v.Kind == ValueKindBoolean {
		return json.Marshal([]any{v.Kind, v.Boolean})
	}
	if v.Kind == ValueKindInline {
		items := v.Inline
		if items == nil {
			items = []InlineItem{}
		}
		return json.Marshal([]any{v.Kind, items})
	}
	if v.Kind == ValueKindList {
		items := v.List
		if items == nil {
			items = []ListItem{}
		}
		return json.Marshal([]any{v.Kind, items})
	}
	if v.Kind == ValueKindObject {
		fields := v.Object
		if fields == nil {
			fields = []ObjectField{}
		}
		return json.Marshal([]any{v.Kind, fields})
	}
	return json.Marshal([]any{v.Kind, v.Text})
}

func (v *Value) UnmarshalJSON(data []byte) error {
	*v = Value{}
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) != 2 {
		return errors.New("compact value must contain exactly two items")
	}
	if err := json.Unmarshal(parts[0], &v.Kind); err != nil {
		return err
	}
	switch v.Kind {
	case ValueKindBoolean:
		if err := json.Unmarshal(parts[1], &v.Boolean); err != nil {
			return err
		}
	case ValueKindInline:
		if err := json.Unmarshal(parts[1], &v.Inline); err != nil {
			return err
		}
	case ValueKindList:
		if err := json.Unmarshal(parts[1], &v.List); err != nil {
			return err
		}
	case ValueKindObject:
		if err := json.Unmarshal(parts[1], &v.Object); err != nil {
			return err
		}
	default:
		if err := json.Unmarshal(parts[1], &v.Text); err != nil {
			return err
		}
	}
	return v.validate()
}

func (i InlineItem) MarshalJSON() ([]byte, error) {
	if err := i.validate(0); err != nil {
		return nil, err
	}
	switch i.Kind {
	case InlineKindText, InlineKindMath, InlineKindPlaceholder:
		return json.Marshal([]any{i.Kind, i.Text})
	case InlineKindBold, InlineKindItalic, InlineKindUnderline, InlineKindStrike, InlineKindCode:
		return json.Marshal([]any{i.Kind, i.Children})
	case InlineKindTextColor, InlineKindBackground:
		return json.Marshal([]any{i.Kind, i.Target, i.Children})
	case InlineKindLink:
		return json.Marshal([]any{i.Kind, i.Target, i.Children})
	case InlineKindHardBreak:
		return json.Marshal([]any{i.Kind})
	default:
		return nil, fmt.Errorf("unsupported inline item kind %q", i.Kind)
	}
}

func (i *InlineItem) UnmarshalJSON(data []byte) error {
	*i = InlineItem{}
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) == 0 {
		return errors.New("compact inline item cannot be empty")
	}
	if err := json.Unmarshal(parts[0], &i.Kind); err != nil {
		return err
	}
	decode := func(want int, targets ...any) error {
		if len(parts) != want {
			return fmt.Errorf("inline item %q must contain exactly %d items", i.Kind, want)
		}
		for index, target := range targets {
			if err := json.Unmarshal(parts[index+1], target); err != nil {
				return fmt.Errorf("inline item %q item %d: %w", i.Kind, index+1, err)
			}
		}
		return nil
	}
	switch i.Kind {
	case InlineKindText, InlineKindMath, InlineKindPlaceholder:
		if err := decode(2, &i.Text); err != nil {
			return err
		}
	case InlineKindBold, InlineKindItalic, InlineKindUnderline, InlineKindStrike, InlineKindCode:
		if err := decode(2, &i.Children); err != nil {
			return err
		}
	case InlineKindTextColor, InlineKindBackground, InlineKindLink:
		if err := decode(3, &i.Target, &i.Children); err != nil {
			return err
		}
	case InlineKindHardBreak:
		if err := decode(1); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported inline item kind %q", i.Kind)
	}
	return i.validate(0)
}

func (i ListItem) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{i.ID, i.Value})
}

func (i *ListItem) UnmarshalJSON(data []byte) error {
	*i = ListItem{}
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) != 2 {
		return errors.New("compact list item must contain exactly two items")
	}
	if err := json.Unmarshal(parts[0], &i.ID); err != nil {
		return err
	}
	return json.Unmarshal(parts[1], &i.Value)
}

func (f ObjectField) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{f.ID, f.Value})
}

func (f *ObjectField) UnmarshalJSON(data []byte) error {
	*f = ObjectField{}
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) != 2 {
		return errors.New("compact object field must contain exactly two items")
	}
	if err := json.Unmarshal(parts[0], &f.ID); err != nil {
		return err
	}
	return json.Unmarshal(parts[1], &f.Value)
}

func (f FieldValue) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{f.ID, f.Value})
}

func (f *FieldValue) UnmarshalJSON(data []byte) error {
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) != 2 {
		return errors.New("compact field must contain exactly two items")
	}
	if err := json.Unmarshal(parts[0], &f.ID); err != nil {
		return err
	}
	return json.Unmarshal(parts[1], &f.Value)
}

func (f FileBinding) MarshalJSON() ([]byte, error) {
	if len(f.Path) != 0 {
		return json.Marshal([]any{f.Field, f.Path, f.File})
	}
	return json.Marshal([]any{f.Field, f.File})
}

func (f *FileBinding) UnmarshalJSON(data []byte) error {
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) != 2 && len(parts) != 3 {
		return errors.New("compact file binding must contain a field, optional typed path, and File handle")
	}
	if err := json.Unmarshal(parts[0], &f.Field); err != nil {
		return err
	}
	if len(parts) == 2 {
		return json.Unmarshal(parts[1], &f.File)
	}
	if err := json.Unmarshal(parts[1], &f.Path); err != nil {
		return err
	}
	return json.Unmarshal(parts[2], &f.File)
}

func (n Node) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{n.ID, n.Kind, n.Parent, n.Order, n.Shared, n.Localized, n.Files, n.Relations})
}

func (n *Node) UnmarshalJSON(data []byte) error {
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) != 8 {
		return errors.New("compact node must contain exactly eight items")
	}
	*n = Node{}
	targets := []any{&n.ID, &n.Kind, &n.Parent, &n.Order, &n.Shared, &n.Localized, &n.Files, &n.Relations}
	for index := range targets {
		if err := json.Unmarshal(parts[index], targets[index]); err != nil {
			return fmt.Errorf("compact node item %d: %w", index, err)
		}
	}
	return nil
}

func (r Relation) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{r.ID, r.Items})
}

func (r *Relation) UnmarshalJSON(data []byte) error {
	*r = Relation{}
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) != 2 {
		return errors.New("compact relation must contain exactly two items")
	}
	if err := json.Unmarshal(parts[0], &r.ID); err != nil {
		return err
	}
	return json.Unmarshal(parts[1], &r.Items)
}

func (i RelationItem) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{i.ID, i.Kind, i.Order, i.Shared, i.Localized, i.Files})
}

func (i *RelationItem) UnmarshalJSON(data []byte) error {
	*i = RelationItem{}
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) != 6 {
		return errors.New("compact relation item must contain exactly six items")
	}
	targets := []any{&i.ID, &i.Kind, &i.Order, &i.Shared, &i.Localized, &i.Files}
	for index := range targets {
		if err := json.Unmarshal(parts[index], targets[index]); err != nil {
			return fmt.Errorf("compact relation item %d: %w", index, err)
		}
	}
	return nil
}
