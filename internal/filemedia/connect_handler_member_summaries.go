package filemedia

import (
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func memberSummaryProto(summaries map[string]MemberSummary, id string) *commonv1.MemberSummary {
	summary, ok := summaries[id]
	if !ok {
		return nil
	}
	return &commonv1.MemberSummary{Id: summary.ID, Nickname: summary.Nickname, AvatarAsset: mediaasset.ReferenceProto(summary.Avatar), Deleted: summary.Deleted}
}
