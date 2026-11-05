package translation

// UnitResult is one translated value mapped back to its original unit id.
type UnitResult struct {
	UnitID         string
	TranslatedText string
	OriginalData   []XLIFFOriginalData
	TargetInline   []XLIFFInline
}
