package public

import openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"

func artistWorkTypeFromDatabase(value string) openv1.WorkType {
	if enumValue, ok := openv1.WorkType_value[value]; ok {
		return openv1.WorkType(enumValue)
	}
	return openv1.WorkType_WORK_TYPE_UNSPECIFIED
}
