package filemedia

import "github.com/google/uuid"

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func IsValidUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}
