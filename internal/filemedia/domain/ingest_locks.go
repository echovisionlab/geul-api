package domain

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/mutation"
)

// IngestSession binds every database phase to the connection holding the
// request's advisory lock while allowing external storage work between phases.
type IngestSession struct {
	Units          mutation.UnitOfWork[Store]
	FaviconCommits FaviconCommits
}

type IngestLocks interface {
	Multipart(context.Context, string, string, func(IngestSession) error) error
	RemoteImport(context.Context, string, func(IngestSession) error) error
}
