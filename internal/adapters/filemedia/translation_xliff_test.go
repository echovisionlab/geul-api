package filemedia

import (
	"context"
	"testing"
	"time"

	filemediadomain "github.com/echovisionlab/geul-api/internal/filemedia"
	translationapp "github.com/echovisionlab/geul-api/internal/translation/application"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type recordingTranslationXLIFFRuntime struct {
	createInput filemediadomain.GeneratedGeneralFileInput
	readFileID  string
	readLimit   int64
	ref         *commonv1.ExpiringMediaRef
	file        filemediadomain.VerifiedFileBody
}

func (r *recordingTranslationXLIFFRuntime) CreateGeneratedGeneralFile(
	_ context.Context,
	input filemediadomain.GeneratedGeneralFileInput,
) (*commonv1.ExpiringMediaRef, error) {
	r.createInput = input
	return r.ref, nil
}

func (r *recordingTranslationXLIFFRuntime) ReadVerifiedFileBody(
	_ context.Context,
	fileID string,
	maximumBytes int64,
) (filemediadomain.VerifiedFileBody, error) {
	r.readFileID, r.readLimit = fileID, maximumBytes
	return r.file, nil
}

func TestTranslationXLIFFFilesDelegatesToExistingFileAuthority(t *testing.T) {
	runtime := &recordingTranslationXLIFFRuntime{
		ref:  &commonv1.ExpiringMediaRef{FileId: "file-a", Url: "https://media.example/file-a", ExpiresAt: timestamppb.New(time.Now().Add(time.Minute))},
		file: filemediadomain.VerifiedFileBody{FileID: "file-b", Extension: "xlf", MimeType: "application/xliff+xml", Body: []byte("<xliff/>")},
	}
	adapter, err := NewTranslationXLIFFFiles(runtime)
	if err != nil {
		t.Fatalf("NewTranslationXLIFFFiles() error = %v", err)
	}
	body := []byte("<xliff>export</xliff>")
	ref, err := adapter.CreateTranslationXLIFF(context.Background(), translationapp.TranslationXLIFFArtifact{
		Body: body, FileName: "post-en.xlf", MimeType: "application/xliff+xml",
	})
	if err != nil || ref != runtime.ref {
		t.Fatalf("CreateTranslationXLIFF() = (%+v, %v)", ref, err)
	}
	if runtime.createInput.Extension != "xlf" || runtime.createInput.FileName != "post-en.xlf" || string(runtime.createInput.Body) != string(body) {
		t.Fatalf("generated File input = %+v", runtime.createInput)
	}
	body[0] = 'X'
	if runtime.createInput.Body[0] == 'X' {
		t.Fatal("adapter retained caller-owned export bytes")
	}

	file, err := adapter.ReadVerifiedTranslationXLIFF(context.Background(), "file-b", 2048)
	if err != nil || file.FileID != "file-b" || string(file.Body) != "<xliff/>" {
		t.Fatalf("ReadVerifiedTranslationXLIFF() = (%+v, %v)", file, err)
	}
	if runtime.readFileID != "file-b" || runtime.readLimit != 2048 {
		t.Fatalf("verified File read = %q/%d", runtime.readFileID, runtime.readLimit)
	}
	runtime.file.Body[0] = 'X'
	if file.Body[0] == 'X' {
		t.Fatal("adapter returned runtime-owned upload bytes")
	}
}

func TestNewTranslationXLIFFFilesRequiresRuntime(t *testing.T) {
	if _, err := NewTranslationXLIFFFiles(nil); err == nil {
		t.Fatal("nil File runtime was accepted")
	}
	var typedNil *recordingTranslationXLIFFRuntime
	if _, err := NewTranslationXLIFFFiles(typedNil); err == nil {
		t.Fatal("typed-nil File runtime was accepted")
	}
}
