package memberref

import commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

// AssetMessage renders an optional public asset reference.
func AssetMessage(asset *Asset) *commonv1.AssetRef {
	if asset == nil {
		return nil
	}
	return &commonv1.AssetRef{AssetId: asset.ID, Url: asset.URL, Extension: asset.Extension, MimeType: asset.MIMEType, FileSize: asset.FileSize, Sha256: asset.SHA256, Disposition: commonv1.AssetDisposition(asset.Disposition), DownloadFilename: asset.DownloadFilename}
}

func Message(summary Summary) *commonv1.MemberSummary {
	return &commonv1.MemberSummary{Id: summary.ID, Nickname: summary.Nickname, AvatarAsset: AssetMessage(summary.Avatar), Deleted: summary.Deleted}
}

func ReferenceMessage(summary *Summary) *commonv1.MemberSummary {
	if summary == nil {
		return nil
	}
	return Message(*summary)
}

func ReferenceMessages(summaries []*Summary) []*commonv1.MemberSummary {
	if summaries == nil {
		return nil
	}
	messages := make([]*commonv1.MemberSummary, len(summaries))
	for index, summary := range summaries {
		messages[index] = ReferenceMessage(summary)
	}
	return messages
}
