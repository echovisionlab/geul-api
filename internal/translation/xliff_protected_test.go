package translation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProtectXLIFFTermsPreservesExactCaseSensitiveOccurrences(t *testing.T) {
	document := XLIFFDocument{
		Version: XLIFFVersion, SourceLocale: "en", TargetLocale: "ko",
		File: XLIFFFile{ID: "protected", Groups: []XLIFFGroup{{
			ID: "body", TranslationUnit: []XLIFFUnit{{
				ID: "u1", Source: "Photoshop and Photoshop, not photoshop or React Native.",
			}},
		}}},
	}
	require.NoError(t, ProtectXLIFFTerms(&document, []string{"Photoshop", "React Native"}))
	unit := document.File.Groups[0].TranslationUnit[0]
	require.Equal(t, "Photoshop and Photoshop, not photoshop or React Native.", unit.Source)
	require.Len(t, unit.OriginalData, 3)
	require.Equal(t, "Photoshop", unit.OriginalData[0].Value)
	require.Equal(t, "Photoshop", unit.OriginalData[1].Value)
	require.Equal(t, "React Native", unit.OriginalData[2].Value)
	require.NoError(t, ValidateXLIFFDocument(&document, false))

	target := unit.Source
	unit.Target = &target
	unit.TargetInline = unit.SourceInline
	document.File.Groups[0].TranslationUnit[0] = unit
	require.NoError(t, ValidateXLIFFDocument(&document, true))

	document.File.Groups[0].TranslationUnit[0].TargetInline = document.File.Groups[0].TranslationUnit[0].TargetInline[1:]
	require.Error(t, ValidateXLIFFDocument(&document, true))
}

func TestProtectXLIFFTermsPreservesOccurrenceSplitAcrossPairedCodes(t *testing.T) {
	document := XLIFFDocument{Version: XLIFFVersion, SourceLocale: "en", TargetLocale: "ko", File: XLIFFFile{
		ID: "post:1", Groups: []XLIFFGroup{{ID: "body", TranslationUnit: []XLIFFUnit{{
			ID: "u1", Source: "React Native", OriginalData: []XLIFFOriginalData{{ID: "d1"}, {ID: "d2"}, {ID: "d3"}, {ID: "d4"}},
			SourceInline: []XLIFFInline{
				{Kind: XLIFFInlinePairedCode, ID: "r1", DataRefStart: "d1", DataRefEnd: "d2", Children: []XLIFFInline{{Kind: XLIFFInlineText, Text: "React "}}},
				{Kind: XLIFFInlinePairedCode, ID: "r2", DataRefStart: "d3", DataRefEnd: "d4", Children: []XLIFFInline{{Kind: XLIFFInlineText, Text: "Native"}}},
			},
		}}}},
	}}
	require.NoError(t, ProtectXLIFFTerms(&document, []string{"React Native"}))
	unit := document.File.Groups[0].TranslationUnit[0]
	require.Equal(t, "protectedTerm1Part1", unit.SourceInline[0].Children[0].ID)
	require.Equal(t, "React ", unit.OriginalData[4].Value)
	require.Equal(t, "protectedTerm1Part2", unit.SourceInline[1].Children[0].ID)
	require.Equal(t, "Native", unit.OriginalData[5].Value)
	target := unit.Source
	unit.Target = &target
	unit.TargetInline = unit.SourceInline
	require.NoError(t, validateXLIFFUnitInline(unit, true))

	unit.TargetInline = []XLIFFInline{unit.SourceInline[1], unit.SourceInline[0]}
	reordered, err := ProjectXLIFFInline(unit.TargetInline, unit.OriginalData)
	require.NoError(t, err)
	unit.Target = &reordered
	require.ErrorContains(t, validateXLIFFUnitInline(unit, true), "protected term parts changed order")
}

func TestProtectXLIFFTermsRejectsInsertedTextBetweenPartsAndHandlesRepeatedOccurrences(t *testing.T) {
	document := XLIFFDocument{Version: XLIFFVersion, SourceLocale: "en", TargetLocale: "ko", File: XLIFFFile{
		ID: "post:1", Groups: []XLIFFGroup{{ID: "body", TranslationUnit: []XLIFFUnit{{
			ID: "u1", Source: "Use React Native and React Native today", OriginalData: []XLIFFOriginalData{{ID: "d1"}, {ID: "d2"}, {ID: "d3"}, {ID: "d4"}, {ID: "d5"}, {ID: "d6"}},
			SourceInline: []XLIFFInline{
				{Kind: XLIFFInlinePairedCode, ID: "r1", DataRefStart: "d1", DataRefEnd: "d2", Children: []XLIFFInline{{Kind: XLIFFInlineText, Text: "Use React "}}},
				{Kind: XLIFFInlinePairedCode, ID: "r2", DataRefStart: "d3", DataRefEnd: "d4", Children: []XLIFFInline{{Kind: XLIFFInlineText, Text: "Native and React "}}},
				{Kind: XLIFFInlinePairedCode, ID: "r3", DataRefStart: "d5", DataRefEnd: "d6", Children: []XLIFFInline{{Kind: XLIFFInlineText, Text: "Native today"}}},
			},
		}}}},
	}}
	require.NoError(t, ProtectXLIFFTerms(&document, []string{"React Native"}))
	unit := document.File.Groups[0].TranslationUnit[0]
	require.Len(t, unit.OriginalData, 10)
	require.Equal(t, "React ", unit.OriginalData[6].Value)
	require.Equal(t, "Native", unit.OriginalData[7].Value)
	require.Equal(t, "React ", unit.OriginalData[8].Value)
	require.Equal(t, "Native", unit.OriginalData[9].Value)
	fragment, err := MarshalXLIFFInlineFragment(unit.SourceInline)
	require.NoError(t, err)
	targetInline, err := UnmarshalXLIFFInlineFragment(fragment)
	require.NoError(t, err)
	targetInline[0].Children = append(targetInline[0].Children, XLIFFInline{Kind: XLIFFInlineText, Text: "unexpected"})
	target := "Use React unexpectedNative and React Native today"
	unit.Target = &target
	unit.TargetInline = targetInline
	require.ErrorContains(t, validateXLIFFUnitInline(unit, true), "protected term parts are no longer adjacent")

	targetInline, err = UnmarshalXLIFFInlineFragment(fragment)
	require.NoError(t, err)
	targetInline[2].Children = targetInline[2].Children[1:]
	target, err = ProjectXLIFFInline(targetInline, unit.OriginalData)
	require.NoError(t, err)
	unit.Target = &target
	unit.TargetInline = targetInline
	require.ErrorContains(t, validateXLIFFUnitInline(unit, true), "inline code identity or nesting changed")
}
