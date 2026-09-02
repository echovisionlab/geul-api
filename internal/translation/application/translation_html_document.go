package application

import "github.com/echovisionlab/geul-api/internal/translation"

func applyHTMLTranslationCandidate(
	source *translation.SourceDocument,
	resultByUnit map[string]translation.UnitResult,
) (*string, *string, error) {
	if source == nil || source.ContentHTML == nil {
		return nil, nil, nil
	}
	return translation.ApplyHTMLCandidate(*source.ContentHTML, resultByUnit)
}
