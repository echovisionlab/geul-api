package mcp

import "github.com/echovisionlab/geul-api/internal/uuidutil"

func canonicalUUID(value string) bool {
	_, err := uuidutil.ParseCanonical(value, "UUID")
	return err == nil
}

func validateUUID(name, value string) error {
	_, err := uuidutil.ParseCanonical(value, name)
	return err
}
