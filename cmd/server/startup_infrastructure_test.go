package main

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMultipartPresignerUsesPublicS3Endpoint(t *testing.T) {
	cfg := &config.Config{
		S3Region:          "us-east-1",
		S3AccessKeyID:     "test-access-key",
		S3SecretAccessKey: "test-secret-key",
		S3ForcePathStyle:  true,
	}
	client, err := newApplicationS3ClientForEndpoint(t.Context(), cfg, "https://s3.geul.example")
	require.NoError(t, err)
	presigned, err := s3.NewPresignClient(client).PresignUploadPart(t.Context(), &s3.UploadPartInput{
		Bucket:        aws.String("geul"),
		Key:           aws.String("file/test.bin"),
		UploadId:      aws.String("upload-id"),
		PartNumber:    aws.Int32(1),
		ContentLength: aws.Int64(10),
	})
	require.NoError(t, err)
	parsed, err := url.Parse(presigned.URL)
	require.NoError(t, err)
	require.Equal(t, "s3.geul.example", parsed.Host)
	require.Equal(t, "/geul/file/test.bin", parsed.Path)
}

func TestDatabaseTracingDoesNotInterpolateQueryVariables(t *testing.T) {
	const sensitiveValue = "person@example.com"
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	db, err := gorm.Open(sqlite.Open("file:telemetry-query-redaction?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Use(newDatabaseTracingPlugin()))

	type account struct {
		ID    uint
		Email string
	}
	require.NoError(t, db.AutoMigrate(&account{}))
	require.NoError(t, db.Create(&account{Email: sensitiveValue}).Error)
	var result account
	require.NoError(t, db.Where("email = ?", sensitiveValue).First(&result).Error)

	for _, span := range recorder.Ended() {
		for _, attr := range span.Attributes() {
			require.Falsef(
				t,
				strings.Contains(attr.Value.String(), sensitiveValue),
				"query variable leaked through span %q attribute %q",
				span.Name(),
				attr.Key,
			)
		}
	}
}
