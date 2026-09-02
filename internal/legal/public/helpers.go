package public

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
