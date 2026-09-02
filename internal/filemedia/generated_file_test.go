package filemedia

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"gorm.io/gorm"
)

func TestCreateGeneratedGeneralFileRejectsInvalidArtifactBeforeStorage(t *testing.T) {
	service := &FileService{db: &gorm.DB{}, s3Client: &s3.Client{}, s3Bucket: "files"}
	for name, input := range map[string]GeneratedGeneralFileInput{
		"missing name":      {Extension: "xlf", MimeType: "application/xliff+xml", Body: []byte("body")},
		"missing extension": {FileName: "export.xlf", MimeType: "application/xliff+xml", Body: []byte("body")},
		"missing MIME":      {FileName: "export.xlf", Extension: "xlf", Body: []byte("body")},
		"empty body":        {FileName: "export.xlf", Extension: "xlf", MimeType: "application/xliff+xml"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.CreateGeneratedGeneralFile(context.Background(), input); err == nil {
				t.Fatal("invalid generated File artifact was accepted")
			}
		})
	}
}

func TestReadVerifiedFileBodyRequiresPositiveBoundBeforeDelivery(t *testing.T) {
	service := &FileService{db: &gorm.DB{}, s3Client: &s3.Client{}, s3Bucket: "files"}
	if _, err := service.ReadVerifiedFileBody(context.Background(), "file-id", 0); err == nil {
		t.Fatal("unbounded verified File read was accepted")
	}
}
