package emailauthoring

import (
	emailutil "github.com/echovisionlab/geul-api/internal/email"
)

// layoutProviderApply is a provider translation's current and patched unit
// values under the locked layout authority.
type layoutProviderApply struct {
	authority  LayoutAuthority
	sourceHTML string
	entry      *emailutil.LayoutTranslationEntry
	current    map[string]string
	next       map[string]string
}
