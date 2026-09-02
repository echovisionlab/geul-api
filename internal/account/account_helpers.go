package account

func ptrStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
