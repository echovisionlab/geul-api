package public

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestArtistWorkTypeFromDatabase(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  openv1.WorkType
	}{
		{"music project", managev1.WorkType_WORK_TYPE_MUSIC_PROJECT.String(), openv1.WorkType_WORK_TYPE_MUSIC_PROJECT},
		{"portfolio", managev1.WorkType_WORK_TYPE_PORTFOLIO.String(), openv1.WorkType_WORK_TYPE_PORTFOLIO},
		{"article", managev1.WorkType_WORK_TYPE_ARTICLE.String(), openv1.WorkType_WORK_TYPE_ARTICLE},
		{"contribution", managev1.WorkType_WORK_TYPE_CONTRIBUTION.String(), openv1.WorkType_WORK_TYPE_CONTRIBUTION},
		{"unknown database value", "WORK_TYPE_FUTURE", openv1.WorkType_WORK_TYPE_UNSPECIFIED},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := artistWorkTypeFromDatabase(test.value); got != test.want {
				t.Fatalf("artistWorkTypeFromDatabase(%q) = %s, want %s", test.value, got, test.want)
			}
		})
	}
}
