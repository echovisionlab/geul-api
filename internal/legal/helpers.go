package legal

func sameNullableString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func ptrStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
