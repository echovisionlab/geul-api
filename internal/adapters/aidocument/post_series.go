package aidocumentadapter

import (
	"errors"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	seriesdomain "github.com/echovisionlab/geul-api/internal/series"
)

// NewPostSeriesRegistration binds the shared DCDP registry to the Post
// Series-owned authorization, lifecycle, projection and mutation facade.
func NewPostSeriesRegistration(
	port *seriesdomain.AIDocumentService,
) (DomainRegistration, error) {
	if port == nil {
		return DomainRegistration{}, errors.New("post series AI document port is required")
	}
	return DomainRegistration{Domain: core.DomainPostSeries, Port: port}, nil
}
