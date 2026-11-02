package release

import errs "github.com/echovisionlab/geul-api/internal/errors"

// connectError gives a Release domain answer the Connect form that callers
// in domains still on the transport branch on; an unclassified error passes
// through.
func connectError(err error) error {
	if classified, ok := errs.Classified(err); ok {
		return classified
	}
	return err
}
