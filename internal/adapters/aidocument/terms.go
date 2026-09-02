package aidocumentadapter

import (
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/legal"
)

func NewTermsRegistration(application *legal.AIDocumentService) (DomainRegistration, error) {
	return newLegalRegistration(core.DomainTerms, "terms", application)
}
