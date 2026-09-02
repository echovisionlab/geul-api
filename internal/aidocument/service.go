package aidocument

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type ReadMode string

const (
	ReadOutline ReadMode = "outline"
	ReadBlocks  ReadMode = "blocks"
	ReadFields  ReadMode = "fields"
)

func (m ReadMode) valid() bool {
	return m == ReadOutline || m == ReadBlocks || m == ReadFields
}

type FieldSelection struct {
	Block    BlockID
	Relation RelationID
	Item     RelationItemID
	Field    FieldID
}

func (s FieldSelection) target() FieldTarget {
	return FieldTarget{Block: s.Block, Relation: s.Relation, Item: s.Item, Field: s.Field}
}

type OpenRequest struct {
	Document DocumentIdentity
	Locale   Locale
}

// OpenMetadata is the compact protocol discovery result before any content
// projection is selected.
type OpenMetadata struct {
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
}

func (m OpenMetadata) Identity() DocumentIdentity {
	return DocumentIdentity{Domain: m.Profile, Reference: m.Document}
}

func (m OpenMetadata) validate() error {
	if m.Protocol != ProtocolVersion {
		return fmt.Errorf("unsupported protocol %q", m.Protocol)
	}
	if err := m.Identity().validate(); err != nil {
		return err
	}
	if err := validateOpaque("catalog fingerprint", m.Catalog, 256); err != nil {
		return err
	}
	if err := validateOpaque("document revision", string(m.DocumentRevision), 256); err != nil {
		return err
	}
	if m.TargetRevision != nil {
		if err := validateOpaque("target revision", string(*m.TargetRevision), 256); err != nil {
			return err
		}
	}
	if err := validateLocale(m.SourceLocale); err != nil {
		return err
	}
	if err := validateLocale(m.Locale); err != nil {
		return err
	}
	if m.LocaleRole != deriveLocaleRole(m.SourceLocale, m.Locale) {
		return errors.New("locale role does not match source and requested locales")
	}
	if m.LocaleRole == LocaleRoleSource && !m.LocaleExists {
		return errors.New("source locale must exist")
	}
	if m.LocaleRole == LocaleRoleSource && m.TargetRevision != nil {
		return errors.New("source locale cannot carry a target revision")
	}
	if m.LocaleRole == LocaleRoleNonSource && m.LocaleExists && m.TargetRevision == nil {
		return errors.New("existing target locale requires a target revision")
	}
	if m.LocaleRole == LocaleRoleNonSource && !m.LocaleExists && m.TargetRevision != nil {
		return errors.New("absent target locale cannot carry a target revision")
	}
	return nil
}

type ReadRequest struct {
	Document DocumentIdentity
	Locale   Locale
	Mode     ReadMode
	Blocks   []BlockID
	Fields   []FieldSelection
	Limit    int
	Cursor   Cursor
}

type Change struct {
	Operation       int
	Kind            OperationKind
	AffectedHandles []string
}

type ApplyResult struct {
	DocumentRevision Revision
	TargetRevision   *Revision
	Changed          bool
	Changes          []Change
	// Normalized is the exact canonical operation batch accepted by the locked
	// owning-domain transaction. Interactive relays must use this authority and
	// must never fall back to the caller's raw request operations.
	Normalized []Operation
}

// ValidatedApply is the only mutation command the application core sends to
// an owning domain. The domain port must repeat both expected revisions as
// atomic CAS observations in the same transaction as all Operations.
type ValidatedApply struct {
	Document   DocumentIdentity
	Locale     Locale
	LocaleRole LocaleRole
	// LocaleExists is the loaded aggregate observation, not separately
	// persisted core state. When false for a non-source locale with value
	// operations, Apply must create the owning-domain translation resource and
	// write its values in the same document and target revision CAS transaction.
	LocaleExists             bool
	ExpectedDocumentRevision Revision
	ExpectedTargetRevision   *Revision
	Operations               []Operation
	AffectedHandles          []string
}

// ValidateLoadedApply normalizes one request against a document that the
// owning domain has already authorized and loaded. It performs no I/O and no
// authorization. ExactMutationPort implementations call it from their
// compiler callback inside the locked owning-domain transaction.
func ValidateLoadedApply(document Document, request ApplyRequest) (ValidatedApply, ValidationResult) {
	validation := ValidateOperations(document, request)
	if !validation.Valid() {
		return ValidatedApply{}, validation
	}
	return ValidatedApply{
		Document:                 request.Identity(),
		Locale:                   request.Locale,
		LocaleRole:               document.Role(),
		LocaleExists:             document.LocaleExists,
		ExpectedDocumentRevision: request.ExpectedDocumentRevision,
		ExpectedTargetRevision:   cloneRevision(request.ExpectedTargetRevision),
		Operations:               validation.Normalized,
		AffectedHandles:          affectedHandles(request),
	}, validation
}

// ExactMutationPort is the mutation boundary for an owning domain.
// Implementations must load and lock the owning root, choose the one
// status-aware Edit/EditArchived action, authorize it exactly once, and only
// then project the current document for normalization and domain compilation.
// The compiler and persistence step run inside that same owning-domain
// transaction. ValidateMutation executes the identical path and rolls the
// transaction back after validation; it must not call the public read Load
// path first.
type ExactMutationPort interface {
	ValidateMutation(context.Context, ApplyRequest) (ValidationResult, error)
	ExecuteMutation(context.Context, ApplyRequest) (ApplyResult, error)
}

// DomainPort is implemented by every owning domain. Load is the authorized
// public read projection. Mutations have no Load-then-Apply compatibility
// path: every implementation must provide the exact locked transaction seam.
type DomainPort interface {
	ExactMutationPort
	Load(context.Context, DocumentIdentity, Locale) (Document, error)
}

type Service struct{ port DomainPort }

func NewService(port DomainPort) (*Service, error) {
	if port == nil {
		return nil, errors.New("AI document domain port is required")
	}
	return &Service{port: port}, nil
}

type CursorError struct {
	Code                    string
	CurrentDocumentRevision Revision
	CurrentTargetRevision   *Revision
}

func (e *CursorError) Error() string {
	return fmt.Sprintf("%s: current document revision is %q", e.Code, e.CurrentDocumentRevision)
}

func (s *Service) Open(ctx context.Context, request OpenRequest) (OpenMetadata, error) {
	if err := request.Document.validate(); err != nil {
		return OpenMetadata{}, err
	}
	if err := validateLocale(request.Locale); err != nil {
		return OpenMetadata{}, err
	}
	document, err := s.port.Load(ctx, request.Document, request.Locale)
	if err != nil {
		return OpenMetadata{}, err
	}
	if err := document.validate(); err != nil {
		return OpenMetadata{}, fmt.Errorf("load AI document: %w", err)
	}
	if document.Identity != request.Document || document.Locale != request.Locale {
		return OpenMetadata{}, errors.New("domain port returned a different document identity or locale")
	}
	return OpenMetadata{
		Protocol: ProtocolVersion, Profile: document.Identity.Domain, Catalog: document.Catalog.Fingerprint,
		Document: document.Identity.Reference, DocumentRevision: document.DocumentRevision,
		TargetRevision: cloneRevision(document.TargetRevision), SourceLocale: document.SourceLocale,
		Locale: document.Locale, LocaleRole: document.Role(), LocaleExists: document.LocaleExists,
	}, nil
}

func (s *Service) Read(ctx context.Context, request ReadRequest) (Projection, error) {
	if err := validateReadRequest(request); err != nil {
		return Projection{}, err
	}
	document, err := s.port.Load(ctx, request.Document, request.Locale)
	if err != nil {
		return Projection{}, err
	}
	if err := document.validate(); err != nil {
		return Projection{}, fmt.Errorf("load AI document: %w", err)
	}
	if document.Identity != request.Document || document.Locale != request.Locale {
		return Projection{}, errors.New("domain port returned a different document identity or locale")
	}

	nodes, err := selectNodes(document, request)
	if err != nil {
		return Projection{}, err
	}
	nodes = canonicalNodes(nodes)
	offset := 0
	fingerprint := readCursorFingerprint(document, request)
	if request.Cursor != "" {
		offset, err = decodeCursor(request.Cursor, fingerprint)
		if err != nil || offset > len(nodes) {
			return Projection{}, &CursorError{
				Code: "stale_cursor", CurrentDocumentRevision: document.DocumentRevision,
				CurrentTargetRevision: cloneRevision(document.TargetRevision),
			}
		}
	}
	limit := request.Limit
	if limit == 0 {
		limit = 64
	}
	end := min(offset+limit, len(nodes))
	page := nodes[offset:end]
	var next *Cursor
	if end < len(nodes) {
		cursor := encodeCursor(end, fingerprint)
		next = &cursor
	}
	return Projection{
		Protocol:         ProtocolVersion,
		Profile:          document.Identity.Domain,
		Catalog:          document.Catalog.Fingerprint,
		Document:         document.Identity.Reference,
		DocumentRevision: document.DocumentRevision,
		TargetRevision:   cloneRevision(document.TargetRevision),
		SourceLocale:     document.SourceLocale,
		Locale:           document.Locale,
		LocaleRole:       document.Role(),
		LocaleExists:     document.LocaleExists,
		Mode:             request.Mode,
		Nodes:            page,
		Next:             next,
	}, nil
}

func (s *Service) Validate(ctx context.Context, request ApplyRequest) (ValidationResult, error) {
	if err := request.validateEnvelope(); err != nil {
		return ValidationResult{}, err
	}
	return s.port.ValidateMutation(ctx, request)
}

func (s *Service) Apply(ctx context.Context, request ApplyRequest) (ApplyResult, error) {
	if err := request.validateEnvelope(); err != nil {
		return ApplyResult{}, err
	}
	result, err := s.port.ExecuteMutation(ctx, request)
	if err != nil {
		return ApplyResult{}, err
	}
	return validateApplyResult(request, result)
}

// AcceptValidatedApply binds the canonical command accepted inside the locked
// owning-domain transaction to its persisted result. Domain ports call this
// before returning so downstream interactive orchestration never has to reuse
// the raw request operation batch.
func AcceptValidatedApply(command ValidatedApply, result ApplyResult) (ApplyResult, error) {
	if len(command.Operations) == 0 {
		return ApplyResult{}, errors.New("domain port accepted mutation without normalized operations")
	}
	result.Normalized = append([]Operation(nil), command.Operations...)
	return validateAcceptedApply(
		command.ExpectedDocumentRevision,
		command.ExpectedTargetRevision,
		result,
	)
}

func validateApplyResult(request ApplyRequest, result ApplyResult) (ApplyResult, error) {
	return validateAcceptedApply(
		request.ExpectedDocumentRevision,
		request.ExpectedTargetRevision,
		result,
	)
}

func validateAcceptedApply(
	expectedDocumentRevision Revision,
	expectedTargetRevision *Revision,
	result ApplyResult,
) (ApplyResult, error) {
	if result.DocumentRevision == "" {
		return ApplyResult{}, errors.New("domain port accepted mutation without a document revision")
	}
	if result.TargetRevision != nil && *result.TargetRevision == "" {
		return ApplyResult{}, errors.New("domain port accepted mutation with an empty target revision")
	}
	if len(result.Normalized) == 0 {
		return ApplyResult{}, errors.New("domain port accepted mutation without normalized operations")
	}
	if err := validateCanonicalOperations(result.Normalized); err != nil {
		return ApplyResult{}, fmt.Errorf("domain port accepted invalid normalized operations: %w", err)
	}
	seenChanges := make(map[int]struct{}, len(result.Changes))
	for _, change := range result.Changes {
		if change.Operation < 0 || change.Operation >= len(result.Normalized) {
			return ApplyResult{}, errors.New("domain port accepted change outside the normalized operation batch")
		}
		if result.Normalized[change.Operation].Kind != change.Kind {
			return ApplyResult{}, errors.New("domain port accepted change kind does not match its normalized operation")
		}
		if _, exists := seenChanges[change.Operation]; exists {
			return ApplyResult{}, errors.New("domain port accepted duplicate changes for one normalized operation")
		}
		seenChanges[change.Operation] = struct{}{}
	}
	if result.Changed {
		if len(result.Changes) == 0 {
			return ApplyResult{}, errors.New("domain port reported a changed mutation without accepted changes")
		}
		switch {
		case result.TargetRevision != nil:
			if result.DocumentRevision != expectedDocumentRevision {
				return ApplyResult{}, errors.New("domain port advanced the document revision for a target-only mutation")
			}
			if expectedTargetRevision != nil && *result.TargetRevision == *expectedTargetRevision {
				return ApplyResult{}, errors.New("domain port reported a changed target mutation without a new target revision")
			}
		case isDeleteTranslationOperations(result.Normalized):
			if result.DocumentRevision != expectedDocumentRevision {
				return ApplyResult{}, errors.New("domain port advanced the document revision for target deletion")
			}
		default:
			if result.DocumentRevision == expectedDocumentRevision {
				return ApplyResult{}, errors.New("domain port reported a changed source mutation without a new document revision")
			}
		}
	} else {
		if result.DocumentRevision != expectedDocumentRevision || !equalRevision(result.TargetRevision, expectedTargetRevision) {
			return ApplyResult{}, errors.New("domain port advanced a revision for a semantic no-op")
		}
		if len(result.Changes) != 0 {
			return ApplyResult{}, errors.New("domain port returned accepted changes for a semantic no-op")
		}
	}
	return result, nil
}

func isDeleteTranslationOperations(operations []Operation) bool {
	return len(operations) == 1 && operations[0].Kind == OperationDeleteTranslation
}

func equalRevision(left, right *Revision) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneRevision(value *Revision) *Revision {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func validateReadRequest(request ReadRequest) error {
	if err := request.Document.validate(); err != nil {
		return err
	}
	if err := validateLocale(request.Locale); err != nil {
		return err
	}
	if !request.Mode.valid() {
		return fmt.Errorf("unsupported read mode %q", request.Mode)
	}
	if request.Limit < 0 || request.Limit > 256 {
		return errors.New("read limit must be between 0 and 256")
	}
	switch request.Mode {
	case ReadOutline:
		if len(request.Blocks) != 0 || len(request.Fields) != 0 {
			return errors.New("outline read does not accept block or field selectors")
		}
	case ReadBlocks:
		if len(request.Blocks) == 0 || len(request.Fields) != 0 {
			return errors.New("block read requires block selectors only")
		}
	case ReadFields:
		if len(request.Fields) == 0 || len(request.Blocks) != 0 {
			return errors.New("field read requires field selectors only")
		}
	}
	return nil
}

func selectNodes(document Document, request ReadRequest) ([]Node, error) {
	nodes := document.Nodes
	switch request.Mode {
	case ReadOutline:
		result := make([]Node, len(nodes))
		for index, node := range nodes {
			result[index] = Node{ID: node.ID, Kind: node.Kind, Parent: node.Parent, Order: node.Order}
			for _, relation := range node.Relations {
				projected := Relation{ID: relation.ID}
				for _, item := range relation.Items {
					projected.Items = append(projected.Items, RelationItem{ID: item.ID, Kind: item.Kind, Order: item.Order})
				}
				result[index].Relations = append(result[index].Relations, projected)
			}
		}
		return result, nil
	case ReadBlocks:
		selected := make(map[BlockID]struct{}, len(request.Blocks))
		for _, block := range request.Blocks {
			if err := validateStableID("block ID", string(block), 160); err != nil {
				return nil, err
			}
			selected[block] = struct{}{}
		}
		result := make([]Node, 0, len(selected))
		for _, node := range nodes {
			if _, ok := selected[node.ID]; ok {
				result = append(result, node)
				delete(selected, node.ID)
			}
		}
		if len(selected) != 0 {
			return nil, errors.New("one or more selected blocks do not exist")
		}
		return result, nil
	case ReadFields:
		nodeByID := make(map[BlockID]Node, len(nodes))
		for _, node := range nodes {
			nodeByID[node.ID] = node
		}
		rules := make(map[string]FieldRule, len(document.Catalog.Fields))
		for _, rule := range document.Catalog.Fields {
			rules[fieldRuleKey(rule.BlockKind, rule.Field)] = rule
		}
		relationRules := make(map[string]RelationFieldRule, len(document.Catalog.RelationFields))
		for _, rule := range document.Catalog.RelationFields {
			relationRules[relationFieldRuleKey(rule.BlockKind, rule.Relation, rule.ItemKind, rule.Field)] = rule
		}
		type selectedFields struct {
			blockFields    map[FieldID]struct{}
			relationFields map[RelationID]map[RelationItemID]map[FieldID]struct{}
		}
		selected := make(map[BlockID]*selectedFields, len(request.Fields))
		for _, field := range request.Fields {
			if err := validateCanonicalFieldTarget(field.target()); err != nil {
				return nil, err
			}
			node, exists := nodeByID[field.Block]
			if !exists {
				return nil, fmt.Errorf("selected block %q does not exist", field.Block)
			}
			entry := selected[field.Block]
			if entry == nil {
				entry = &selectedFields{blockFields: make(map[FieldID]struct{}), relationFields: make(map[RelationID]map[RelationItemID]map[FieldID]struct{})}
				selected[field.Block] = entry
			}
			if field.Relation == "" {
				if _, known := rules[fieldRuleKey(node.Kind, field.Field)]; !known {
					return nil, fmt.Errorf("selected field %q is not defined for block %q", field.Field, field.Block)
				}
				entry.blockFields[field.Field] = struct{}{}
				continue
			}
			relation, item, ok := findRelationItem(node, field.Relation, field.Item)
			if !ok {
				return nil, fmt.Errorf("selected relation item %q does not exist", field.Item)
			}
			if _, known := relationRules[relationFieldRuleKey(node.Kind, relation.ID, item.Kind, field.Field)]; !known {
				return nil, fmt.Errorf("selected field %q is not defined for relation item %q", field.Field, field.Item)
			}
			if entry.relationFields[field.Relation] == nil {
				entry.relationFields[field.Relation] = make(map[RelationItemID]map[FieldID]struct{})
			}
			if entry.relationFields[field.Relation][field.Item] == nil {
				entry.relationFields[field.Relation][field.Item] = make(map[FieldID]struct{})
			}
			entry.relationFields[field.Relation][field.Item][field.Field] = struct{}{}
		}
		result := make([]Node, 0, len(selected))
		for _, node := range nodes {
			fields, requested := selected[node.ID]
			if !requested {
				continue
			}
			copy := Node{ID: node.ID, Kind: node.Kind, Parent: node.Parent, Order: node.Order}
			for _, field := range node.Shared {
				if _, ok := fields.blockFields[field.ID]; ok {
					copy.Shared = append(copy.Shared, field)
				}
			}
			for _, field := range node.Localized {
				if _, ok := fields.blockFields[field.ID]; ok {
					copy.Localized = append(copy.Localized, field)
				}
			}
			for _, file := range node.Files {
				if _, ok := fields.blockFields[file.Field]; ok {
					copy.Files = append(copy.Files, file)
				}
			}
			for _, relation := range node.Relations {
				items := fields.relationFields[relation.ID]
				if len(items) == 0 {
					continue
				}
				projectedRelation := Relation{ID: relation.ID}
				for _, item := range relation.Items {
					requestedFields, ok := items[item.ID]
					if !ok {
						continue
					}
					projectedItem := RelationItem{ID: item.ID, Kind: item.Kind, Order: item.Order}
					for _, value := range item.Shared {
						if _, ok := requestedFields[value.ID]; ok {
							projectedItem.Shared = append(projectedItem.Shared, value)
						}
					}
					for _, value := range item.Localized {
						if _, ok := requestedFields[value.ID]; ok {
							projectedItem.Localized = append(projectedItem.Localized, value)
						}
					}
					for _, file := range item.Files {
						if _, ok := requestedFields[file.Field]; ok {
							projectedItem.Files = append(projectedItem.Files, file)
						}
					}
					projectedRelation.Items = append(projectedRelation.Items, projectedItem)
				}
				copy.Relations = append(copy.Relations, projectedRelation)
			}
			// A catalog-known field with no stored value is represented by its
			// containing node with that field absent. This is distinct from an
			// explicit empty value, which remains a present field with "".
			result = append(result, copy)
		}
		return result, nil
	default:
		return nil, errors.New("unsupported read mode")
	}
}

func validateCanonicalFieldTarget(target FieldTarget) error {
	if err := validateStableID("block ID", string(target.Block), 160); err != nil {
		return err
	}
	if target.relationItem() {
		if target.Relation == "" || target.Item == "" {
			return errors.New("relation-item field target requires both relation and item IDs")
		}
		if err := validateStableID("relation ID", string(target.Relation), 120); err != nil {
			return err
		}
		if err := validateStableID("relation item ID", string(target.Item), 160); err != nil {
			return err
		}
	}
	if err := validateStableID("field ID", string(target.Field), 120); err != nil {
		return err
	}
	for _, segment := range target.Path {
		if err := segment.validate(); err != nil {
			return err
		}
	}
	return nil
}

func findRelationItem(node Node, relationID RelationID, itemID RelationItemID) (Relation, RelationItem, bool) {
	for _, relation := range node.Relations {
		if relation.ID != relationID {
			continue
		}
		for _, item := range relation.Items {
			if item.ID == itemID {
				return relation, item, true
			}
		}
		return relation, RelationItem{}, false
	}
	return Relation{}, RelationItem{}, false
}

func readCursorFingerprint(document Document, request ReadRequest) string {
	blocks := append([]BlockID(nil), request.Blocks...)
	sort.Slice(blocks, func(a, b int) bool { return blocks[a] < blocks[b] })
	fields := append([]FieldSelection(nil), request.Fields...)
	sort.Slice(fields, func(a, b int) bool {
		if fields[a].Block != fields[b].Block {
			return fields[a].Block < fields[b].Block
		}
		if fields[a].Relation != fields[b].Relation {
			return fields[a].Relation < fields[b].Relation
		}
		if fields[a].Item != fields[b].Item {
			return fields[a].Item < fields[b].Item
		}
		return fields[a].Field < fields[b].Field
	})
	payload, _ := json.Marshal([]any{
		document.Identity.Domain, document.Identity.Reference, document.DocumentRevision,
		document.TargetRevision,
		document.SourceLocale, document.Locale, request.Mode, blocks, fields,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:12])
}

func encodeCursor(offset int, fingerprint string) Cursor {
	return Cursor("c1." + strconv.Itoa(offset) + "." + fingerprint)
}

func decodeCursor(cursor Cursor, fingerprint string) (int, error) {
	parts := strings.Split(string(cursor), ".")
	if len(parts) != 3 || parts[0] != "c1" || parts[2] != fingerprint {
		return 0, errors.New("cursor binding mismatch")
	}
	offset, err := strconv.Atoi(parts[1])
	if err != nil || offset < 0 {
		return 0, errors.New("invalid cursor offset")
	}
	return offset, nil
}
