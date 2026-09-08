package handler

import (
	"crypto/sha256"
	"os"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func assetWriteResult(target *commonv1.AssetWriteTarget, path string) (*commonv1.AssetWriteResult, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	sum, err := fileSHA256(path)
	if err != nil {
		return nil, err
	}
	return &commonv1.AssetWriteResult{
		AssetId:  target.GetAssetId(),
		FileSize: stat.Size(),
		Sha256:   sum,
	}, nil
}

func fileSHA256(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return sum[:], nil
}
