package release

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

// The answers below keep the messages the Release RPCs have always sent.

func authenticationRequired() *domainerr.Error {
	return domainerr.New(domainerr.Unauthenticated, "", "authentication required")
}

func notAuthenticated() *domainerr.Error {
	return domainerr.New(domainerr.Unauthenticated, "", "not authenticated")
}

func adminRequired() *domainerr.Error {
	return domainerr.Denied("admin access required")
}

func accountBanned() *domainerr.Error {
	return domainerr.Denied("account is banned")
}

func noPermission(action, resource string) *domainerr.Error {
	return domainerr.Denied(fmt.Sprintf("no permission to %s this %s", action, resource))
}

func notFound(message string) *domainerr.Error {
	return domainerr.New(domainerr.NotFound, "", message)
}

func releaseNotFound(releaseID string) *domainerr.Error {
	return domainerr.ResourceNotFound("release", releaseID)
}

func trackNotFound() *domainerr.Error {
	return notFound("track not found")
}

func slugTaken(cause error) *domainerr.Error {
	return domainerr.Duplicate("release", "slug", "slug").WithCause(cause)
}

// missingAs answers notFound for a row lookup that found nothing and keeps
// any other failure.
func missingAs(err error, notFound *domainerr.Error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFound
	}
	return err
}
