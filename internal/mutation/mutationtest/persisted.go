package mutationtest

import (
	"fmt"
	"reflect"
	"time"

	"google.golang.org/protobuf/proto"
)

// ClonePersisted copies an explicitly selected persisted field, including its
// nested maps, slices and pointers. Pass domain values, never a Store or its
// private state struct. Map keys must be immutable values; persisted graphs
// must be acyclic. Protobuf messages use proto.Clone and time.Time is immutable.
// Mutable private fields and functions are rejected instead of being shared.
func ClonePersisted[T any](value T) T {
	source := reflect.ValueOf(value)
	if !source.IsValid() {
		return value
	}
	return clonePersisted(source).Interface().(T)
}

var timeType = reflect.TypeFor[time.Time]()

func clonePersisted(value reflect.Value) reflect.Value {
	if value.Type() == timeType {
		return value
	}
	if value.CanInterface() {
		if message, ok := value.Interface().(proto.Message); ok {
			if value.Kind() == reflect.Pointer && value.IsNil() {
				return value
			}
			return reflect.ValueOf(proto.Clone(message))
		}
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return value
		}
		cloned := reflect.New(value.Type()).Elem()
		cloned.Set(clonePersisted(value.Elem()))
		return cloned
	case reflect.Pointer:
		if value.IsNil() {
			return value
		}
		cloned := reflect.New(value.Type().Elem())
		cloned.Elem().Set(clonePersisted(value.Elem()))
		return cloned
	case reflect.Map:
		if value.IsNil() {
			return value
		}
		cloned := reflect.MakeMapWithSize(value.Type(), value.Len())
		for entries := value.MapRange(); entries.Next(); {
			requireImmutable(entries.Key())
			cloned.SetMapIndex(entries.Key(), clonePersisted(entries.Value()))
		}
		return cloned
	case reflect.Slice:
		if value.IsNil() {
			return value
		}
		cloned := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			cloned.Index(i).Set(clonePersisted(value.Index(i)))
		}
		return cloned
	case reflect.Array:
		cloned := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			cloned.Index(i).Set(clonePersisted(value.Index(i)))
		}
		return cloned
	case reflect.Struct:
		cloned := reflect.New(value.Type()).Elem()
		cloned.Set(value)
		for i := 0; i < value.NumField(); i++ {
			if !value.Type().Field(i).IsExported() {
				requireImmutable(value.Field(i))
				continue
			}
			cloned.Field(i).Set(clonePersisted(value.Field(i)))
		}
		return cloned
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		panic(fmt.Sprintf("mutationtest: %s is not persisted state", value.Type()))
	default:
		return value
	}
}

func requireImmutable(value reflect.Value) {
	if value.Type() == timeType {
		return
	}
	switch value.Kind() {
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			requireImmutable(value.Field(i))
		}
	case reflect.Array:
		for i := 0; i < value.Len(); i++ {
			requireImmutable(value.Index(i))
		}
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		if !value.IsNil() {
			panic(fmt.Sprintf("mutationtest: %s requires an explicit persisted clone", value.Type()))
		}
	}
}
