package filemedia

import (
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func memberSummaryProto(summary *MemberSummary) *commonv1.MemberSummary {
	if summary == nil {
		return nil
	}
	return &commonv1.MemberSummary{Id: summary.ID, Nickname: summary.Nickname, AvatarAsset: mediaasset.ReferenceProto(summary.Avatar), Deleted: summary.Deleted}
}
