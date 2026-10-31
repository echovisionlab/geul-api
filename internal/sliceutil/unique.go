// Package sliceutil holds the slice helpers the standard library lacks.
package sliceutil

import (
	"slices"
	"strings"
)

// UniqueNonZero returns values in first-occurrence order without zero values
// or repeats.
func UniqueNonZero[T comparable](values []T) []T {
	var zero T
	result := make([]T, 0, len(values))
	seen := make(map[T]struct{}, len(values))
	for _, value := range values {
		if value == zero {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// UniqueTrimmed trims each value and returns the non-empty results in
// first-occurrence order without repeats.
func UniqueTrimmed(values []string) []string {
	trimmed := make([]string, len(values))
	for i, value := range values {
		trimmed[i] = strings.TrimSpace(value)
	}
	return UniqueNonZero(trimmed)
}

// SortedUniqueTrimmed returns the distinct non-empty trimmed values in
// ascending order.
func SortedUniqueTrimmed(values []string) []string {
	result := UniqueTrimmed(values)
	slices.Sort(result)
	return result
}
