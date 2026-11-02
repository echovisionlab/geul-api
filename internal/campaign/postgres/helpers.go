package campaignpostgres

import "strings"

func ptrStringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func nullableTrimmedString(v string) *string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return &v
}
