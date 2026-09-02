package translation

import "errors"

const (
	// ContainerTypeEntity identifies fields attached directly to an entity.
	ContainerTypeEntity = "entity"
	// ContainerTypeBlock identifies content block fields.
	ContainerTypeBlock = "block"
	// ContainerTypeSection identifies section fields within structured content.
	ContainerTypeSection = "section"
	// ContainerTypeHTMLNode identifies text extracted from an HTML node.
	ContainerTypeHTMLNode = "html_node"
	// ContainerTypeRelation identifies fields attached to a related entity.
	ContainerTypeRelation = "relation"

	// SourceFormatPlainText identifies source text without markup.
	SourceFormatPlainText = "plain_text"
	// SourceFormatHTMLText identifies source text represented as HTML.
	SourceFormatHTMLText = "html_text"

	// BundleTypeEntity identifies an entity-level provider bundle.
	BundleTypeEntity = "entity"
	// BundleTypeBlock identifies a content-block provider bundle.
	BundleTypeBlock = "block"
	// BundleTypeSection identifies a section provider bundle.
	BundleTypeSection = "section"
	// BundleTypeHTML identifies an HTML provider bundle.
	BundleTypeHTML = "html"
)

// ErrNoTranslatableUnits indicates that a source document contains no units for translation.
var ErrNoTranslatableUnits = errors.New("translation source document does not contain translatable units")

// ErrSourceNoLongerCurrent indicates that an apply or source-locale mutation
// lost the source authority fence captured by the translation operation.
var ErrSourceNoLongerCurrent = errors.New("translation source is no longer current")
