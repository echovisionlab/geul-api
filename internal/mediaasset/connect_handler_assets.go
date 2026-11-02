package mediaasset

import commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

func ReferenceProto(ref *Reference) *commonv1.AssetRef {
	if ref == nil {
		return nil
	}
	return &commonv1.AssetRef{AssetId: ref.AssetID, Url: ref.URL, Extension: ref.Extension, MimeType: ref.MimeType, FileSize: ref.FileSize, Sha256: append([]byte(nil), ref.SHA256...), Disposition: commonv1.AssetDisposition(ref.Disposition), DownloadFilename: ref.DownloadFilename}
}
func WriteTargetProto(target *WriteTarget) *commonv1.AssetWriteTarget {
	if target == nil {
		return nil
	}
	return &commonv1.AssetWriteTarget{AssetId: target.AssetID, ObjectKey: target.ObjectKey, Extension: target.Extension, MimeType: target.MimeType, Disposition: commonv1.AssetDisposition(target.Disposition), DownloadFilename: target.DownloadFilename}
}
func WrittenAssetFromProto(result *commonv1.AssetWriteResult) *WrittenAsset {
	if result == nil {
		return nil
	}
	return &WrittenAsset{AssetID: result.GetAssetId(), FileSize: result.GetFileSize(), SHA256: result.GetSha256()}
}
func WrittenGenerationFromProto(result *commonv1.MediaGenerationWriteResult) *WrittenGeneration {
	if result == nil {
		return nil
	}
	return &WrittenGeneration{GenerationID: result.GetGenerationId(), ManifestSHA256: result.GetManifestSha256(), ObjectCount: result.GetObjectCount(), TotalSize: result.GetTotalSize()}
}

func AllocationResultProto(asset *Asset, target *WriteTarget, err error) (*Asset, *commonv1.AssetWriteTarget, error) {
	return asset, WriteTargetProto(target), err
}
