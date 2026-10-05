package release

import "regexp"

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isValidUUID(value string) bool {
	return uuidPattern.MatchString(value)
}

func IsValidUUID(value string) bool { return isValidUUID(value) }

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}
