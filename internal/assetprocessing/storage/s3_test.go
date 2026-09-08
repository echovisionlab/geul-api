package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	appconfig "github.com/echovisionlab/geul-api/internal/assetprocessing/config"
)

func TestNewS3Client(t *testing.T) {
	t.Run("config error", func(t *testing.T) {
		resetStorageOps(t)
		loadDefaultConfig = func(context.Context, ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
			return aws.Config{}, errors.New("config failed")
		}
		if _, err := NewS3Client(testConfig()); err == nil {
			t.Fatal("expected AWS config error")
		}
	})

	t.Run("configured client", func(t *testing.T) {
		resetStorageOps(t)
		loadDefaultConfig = func(_ context.Context, options ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
			loadOptions := awsconfig.LoadOptions{}
			for _, option := range options {
				if err := option(&loadOptions); err != nil {
					t.Fatalf("load option: %v", err)
				}
			}
			if loadOptions.Region != "us-east-1" || loadOptions.Credentials == nil {
				t.Fatalf("unexpected load options: %#v", loadOptions)
			}
			return aws.Config{}, nil
		}
		client := &s3.Client{}
		newS3FromConfig = func(_ aws.Config, options ...func(*s3.Options)) *s3.Client {
			s3Options := s3.Options{}
			for _, option := range options {
				option(&s3Options)
			}
			if !s3Options.UsePathStyle || s3Options.BaseEndpoint == nil || *s3Options.BaseEndpoint != "http://minio.test" {
				t.Fatalf("unexpected S3 options: %#v", s3Options)
			}
			return client
		}
		got, err := NewS3Client(testConfig())
		if err != nil {
			t.Fatalf("new S3 client: %v", err)
		}
		if got.client != client || got.bucket != "media" {
			t.Fatalf("unexpected client: %#v", got)
		}
	})
}

func TestS3ClientDownload(t *testing.T) {
	client := &S3Client{client: &s3.Client{}, bucket: "media"}

	t.Run("mkdir error", func(t *testing.T) {
		resetStorageOps(t)
		mkdirAll = func(string, os.FileMode) error { return errors.New("mkdir failed") }
		if err := client.Download(context.Background(), "key", filepath.Join(t.TempDir(), "dir", "file")); err == nil {
			t.Fatal("expected mkdir error")
		}
	})

	t.Run("get error", func(t *testing.T) {
		resetStorageOps(t)
		getObject = func(*s3.Client, context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return nil, errors.New("get failed")
		}
		if err := client.Download(context.Background(), "key", filepath.Join(t.TempDir(), "file")); err == nil {
			t.Fatal("expected get error")
		}
	})

	t.Run("create error", func(t *testing.T) {
		resetStorageOps(t)
		getObject = objectWithBody("payload")
		createFile = func(string) (*os.File, error) { return nil, errors.New("create failed") }
		if err := client.Download(context.Background(), "key", filepath.Join(t.TempDir(), "file")); err == nil {
			t.Fatal("expected create error")
		}
	})

	t.Run("copy error", func(t *testing.T) {
		resetStorageOps(t)
		getObject = objectWithBody("payload")
		copyStream = func(io.Writer, io.Reader) (int64, error) { return 0, errors.New("copy failed") }
		if err := client.Download(context.Background(), "key", filepath.Join(t.TempDir(), "file")); err == nil {
			t.Fatal("expected copy error")
		}
	})

	t.Run("success", func(t *testing.T) {
		resetStorageOps(t)
		getObject = objectWithBody("payload")
		path := filepath.Join(t.TempDir(), "nested", "file")
		if err := client.Download(context.Background(), "key", path); err != nil {
			t.Fatalf("download: %v", err)
		}
		payload, err := os.ReadFile(path)
		if err != nil || string(payload) != "payload" {
			t.Fatalf("downloaded payload=%q err=%v", payload, err)
		}
	})
}

func TestS3ClientUploadDeleteAndSize(t *testing.T) {
	client := &S3Client{client: &s3.Client{}, bucket: "media"}
	t.Run("upload failures", func(t *testing.T) { testS3UploadFailures(t, client) })
	t.Run("upload success", func(t *testing.T) { testS3UploadSuccess(t, client) })
	t.Run("delete", func(t *testing.T) { testS3Delete(t, client) })
	t.Run("head", func(t *testing.T) { testS3ObjectSize(t, client) })
}

func testS3UploadFailures(t *testing.T, client *S3Client) {
	resetStorageOps(t)
	if err := client.Upload(context.Background(), "key", "/missing/file", "model/gltf-binary"); err == nil {
		t.Fatal("expected open error")
	}
	resetStorageOps(t)
	closed, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}
	openFile = func(string) (*os.File, error) { return closed, nil }
	if err := client.Upload(context.Background(), "key", "unused", "model/gltf-binary"); err == nil {
		t.Fatal("expected stat error")
	}
}

func testS3UploadSuccess(t *testing.T, client *S3Client) {
	resetStorageOps(t)
	path := filepath.Join(t.TempDir(), "upload.glb")
	if err := os.WriteFile(path, []byte("payload"), 0644); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	putObject = func(*s3.Client, context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
		return nil, errors.New("put failed")
	}
	if err := client.Upload(context.Background(), "key", path, "model/gltf-binary"); err == nil {
		t.Fatal("expected put error")
	}
	putObject = func(_ *s3.Client, _ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
		body, err := io.ReadAll(input.Body)
		if err != nil {
			t.Fatalf("read upload body: %v", err)
		}
		if aws.ToString(input.Bucket) != "media" || aws.ToString(input.Key) != "key" || aws.ToString(input.ContentType) != "model/gltf-binary" || aws.ToInt64(input.ContentLength) != 7 || string(body) != "payload" {
			t.Fatalf("unexpected put input: %#v body=%q", input, body)
		}
		return &s3.PutObjectOutput{}, nil
	}
	if err := client.Upload(context.Background(), "key", path, "model/gltf-binary"); err != nil {
		t.Fatalf("upload: %v", err)
	}
}

func testS3Delete(t *testing.T, client *S3Client) {
	resetStorageOps(t)
	deleteObject = func(_ *s3.Client, _ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
		if aws.ToString(input.Key) != "key" {
			t.Fatalf("unexpected delete key: %q", aws.ToString(input.Key))
		}
		return &s3.DeleteObjectOutput{}, nil
	}
	if err := client.Delete(context.Background(), "key"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	deleteObject = func(*s3.Client, context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
		return nil, errors.New("delete failed")
	}
	if err := client.Delete(context.Background(), "key"); err == nil {
		t.Fatal("expected delete error")
	}
}

func testS3ObjectSize(t *testing.T, client *S3Client) {
	resetStorageOps(t)
	headObject = func(*s3.Client, context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
		return nil, errors.New("head failed")
	}
	if _, err := client.GetObjectSize(context.Background(), "key"); err == nil {
		t.Fatal("expected head error")
	}
	headObject = func(*s3.Client, context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
		return &s3.HeadObjectOutput{}, nil
	}
	if size, err := client.GetObjectSize(context.Background(), "key"); err != nil || size != 0 {
		t.Fatalf("nil content length: size=%d err=%v", size, err)
	}
	headObject = func(_ *s3.Client, _ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
		if aws.ToString(input.Bucket) != "media" || aws.ToString(input.Key) != "key" {
			t.Fatalf("unexpected head input: %#v", input)
		}
		return &s3.HeadObjectOutput{ContentLength: aws.Int64(42)}, nil
	}
	if size, err := client.GetObjectSize(context.Background(), "key"); err != nil || size != 42 {
		t.Fatalf("content length: size=%d err=%v", size, err)
	}
}

func objectWithBody(payload string) func(*s3.Client, context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return func(_ *s3.Client, _ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
		if aws.ToString(input.Bucket) != "media" || aws.ToString(input.Key) != "key" {
			return nil, errors.New("unexpected get input")
		}
		return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(payload))}, nil
	}
}

func testConfig() *appconfig.Config {
	return &appconfig.Config{
		S3Bucket:          "media",
		S3Region:          "us-east-1",
		S3Endpoint:        "http://minio.test",
		S3AccessKeyID:     "access",
		S3SecretAccessKey: "secret",
		S3ForcePathStyle:  true,
	}
}

func resetStorageOps(t *testing.T) {
	t.Helper()
	loadDefaultConfig = awsconfig.LoadDefaultConfig
	newS3FromConfig = s3.NewFromConfig
	mkdirAll = os.MkdirAll
	createFile = os.Create
	openFile = os.Open
	copyStream = io.Copy
	getObject = (*s3.Client).GetObject
	putObject = (*s3.Client).PutObject
	deleteObject = (*s3.Client).DeleteObject
	headObject = (*s3.Client).HeadObject
	t.Cleanup(func() {
		loadDefaultConfig = awsconfig.LoadDefaultConfig
		newS3FromConfig = s3.NewFromConfig
		mkdirAll = os.MkdirAll
		createFile = os.Create
		openFile = os.Open
		copyStream = io.Copy
		getObject = (*s3.Client).GetObject
		putObject = (*s3.Client).PutObject
		deleteObject = (*s3.Client).DeleteObject
		headObject = (*s3.Client).HeadObject
	})
}
