package translation

import (
	"fmt"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// SourceDocument is the normalized source material used by translation extraction.
type SourceDocument struct {
	SourceLocale            string
	Title                   string
	Summary                 *string
	ContentJSON             []byte
	ContentHTML             *string
	ContentText             *string
	ProtectedTerms          []string
	ContentDocumentRevision string
	ContentBlockDocument    *contentv1.LocalizedRichTextDocument
	PageDocument            *contentv1.LocalizedPageDocument
	ReleaseCreditNotes      map[string]string
}

// Candidate is the translated content prepared for persistence.
type Candidate struct {
	Title                     *string
	Summary                   *string
	ContentJSON               []byte
	ContentHTML               *string
	ContentText               *string
	ContentDocumentRevision   string
	ContentBlockLocaleOverlay *contentv1.RichTextLocaleOverlay
	ContentBlockLocaleDeletes []string
	PageDocument              *contentv1.LocalizedPageDocument
	ReleaseCreditNotes        map[string]string
	providerUnitPatch         *ProviderUnitPatch
}

// ProviderUnitPatch is the request-time/current-source unit intersection that
// a late provider response may change. It is deliberately attached only by
// the provider delivery path; interactive interchange keeps its explicit
// PATCH/REPLACE contract.
type ProviderUnitPatch struct {
	Units   []Unit
	Results map[string]UnitResult
}

// SetProviderUnitPatch limits provider persistence to the surviving units in
// plan. Unknown response units are not retained in the persistence patch.
func (c *Candidate) SetProviderUnitPatch(plan *ExtractionPlan, results map[string]UnitResult) error {
	if c == nil || plan == nil {
		return fmt.Errorf("provider candidate and surviving extraction plan are required")
	}
	patch := &ProviderUnitPatch{
		Units:   append([]Unit(nil), plan.Units...),
		Results: make(map[string]UnitResult, len(plan.Units)),
	}
	for _, unit := range plan.Units {
		result, ok := results[unit.UnitID]
		if !ok {
			return fmt.Errorf("provider response is missing surviving unit %q", unit.UnitID)
		}
		patch.Results[unit.UnitID] = result
	}
	c.providerUnitPatch = patch
	return nil
}

// HasProviderUnitPatch reports whether this candidate came from asynchronous
// provider delivery rather than interactive XLIFF interchange.
func (c *Candidate) HasProviderUnitPatch() bool {
	return c != nil && c.providerUnitPatch != nil
}

// ProviderUnitRequested reports whether a surviving provider request owns the
// exact stable unit. Non-provider candidates return false.
func (c *Candidate) ProviderUnitRequested(unitID string) bool {
	if c == nil || c.providerUnitPatch == nil {
		return false
	}
	for _, unit := range c.providerUnitPatch.Units {
		if unit.UnitID == unitID {
			return true
		}
	}
	return false
}

// ProviderPatch returns a defensive copy for a typed domain adapter that must
// compile nested document mutations (Page). Rich Text root adapters should use
// BuildProviderTargetRichTextBatch directly.
func (c *Candidate) ProviderPatch() (*ProviderUnitPatch, bool) {
	if c == nil || c.providerUnitPatch == nil {
		return nil, false
	}
	patch := &ProviderUnitPatch{
		Units:   append([]Unit(nil), c.providerUnitPatch.Units...),
		Results: make(map[string]UnitResult, len(c.providerUnitPatch.Results)),
	}
	for unitID, result := range c.providerUnitPatch.Results {
		patch.Results[unitID] = result
	}
	return patch, true
}

// RichTextLocaleMutations returns one exact locale mutation set: present
// candidate Blocks are upserts (including explicit-empty fields), while
// omitted current Blocks selected for replacement are deletes.
func (c *Candidate) RichTextLocaleMutations() []*contentv1.RichTextBlockLocaleMutation {
	if c == nil {
		return nil
	}
	blocks := c.ContentBlockLocaleOverlay.GetBlocks()
	mutations := make([]*contentv1.RichTextBlockLocaleMutation, 0, len(blocks)+len(c.ContentBlockLocaleDeletes))
	for _, block := range blocks {
		mutations = append(mutations, &contentv1.RichTextBlockLocaleMutation{
			Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{
				Upsert: &contentv1.UpsertRichTextBlockLocale{Block: block},
			},
		})
	}
	for _, blockID := range c.ContentBlockLocaleDeletes {
		mutations = append(mutations, &contentv1.RichTextBlockLocaleMutation{
			Operation: &contentv1.RichTextBlockLocaleMutation_Delete{
				Delete: &contentv1.DeleteRichTextBlockLocale{BlockId: blockID},
			},
		})
	}
	return mutations
}

// Unit is the canonical translation slot extracted from a source document.
type Unit struct {
	UnitID        string
	EntityType    string
	EntityID      string
	Path          string
	ContainerType string
	ContainerID   string
	FieldName     string
	SourceText    string
	SourceFormat  string
	SourceLocale  string
	Context       *string
	OriginalData  []XLIFFOriginalData
	SourceInline  []XLIFFInline
}

// Bundle groups translation units for one provider request context.
type Bundle struct {
	BundleID      string
	EntityType    string
	EntityID      string
	SourceLocale  string
	TargetLocale  string
	BundleType    string
	ContextText   *string
	SequenceIndex int
	SequenceTotal int
	Units         []Unit
}

// ExtractionPlan captures extracted units and bundles for one locale job.
type ExtractionPlan struct {
	EntityType     string
	EntityID       string
	SourceLocale   string
	TargetLocale   string
	ContextTitle   *string
	ProtectedTerms []string
	Units          []Unit
	Bundles        []Bundle
}
