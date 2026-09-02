package series

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func nullableStringEqual(left, right *string) bool {
	return sameOptionalString(left, right)
}
