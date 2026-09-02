// Package dependencycheck validates required constructor dependencies.
package dependencycheck

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/echovisionlab/geul-api/internal/structured"
)

// Validator accumulates constructor dependency validation failures.
type Validator struct {
	serviceName string
	errors      []string
}

// New creates a Validator for serviceName.
func New(serviceName string) *Validator {
	return &Validator{serviceName: serviceName}
}

// RequireNotNil records a missing dependency.
func (v *Validator) RequireNotNil(value structured.Value, name string) *Validator {
	if isNil(value) {
		v.errors = append(v.errors, fmt.Sprintf("%s is required", name))
	}
	return v
}

// Validate panics when a required dependency is missing.
func (v *Validator) Validate() {
	if len(v.errors) > 0 {
		panic(fmt.Sprintf("%s: %s", v.serviceName, strings.Join(v.errors, ", ")))
	}
}

// MustNotNil panics when value is nil, including a typed nil interface.
func MustNotNil(value structured.Value, name string) {
	if isNil(value) {
		panic(fmt.Sprintf("%s is required", name))
	}
}

func isNil(value structured.Value) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	}
	return false
}
