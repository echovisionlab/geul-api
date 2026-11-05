// Package dependencycheck validates required constructor dependencies.
package dependencycheck

import (
	"fmt"
	"reflect"

	"github.com/echovisionlab/geul-api/internal/structured"
)

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
