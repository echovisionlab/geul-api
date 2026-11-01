package filemedia

import (
	"context"
	"fmt"
	"reflect"

	"github.com/echovisionlab/geul-api/internal/mediaauth"

	filemediadomain "github.com/echovisionlab/geul-api/internal/filemedia"
	translationapp "github.com/echovisionlab/geul-api/internal/translation/application"
)

type TranslationXLIFFRuntime interface {
	CreateGeneratedGeneralFile(context.Context, filemediadomain.GeneratedGeneralFileInput) (*mediaauth.Reference, error)
	ReadVerifiedFileBody(context.Context, string, int64) (filemediadomain.VerifiedFileBody, error)
}

// TranslationXLIFFFiles maps Translation's artifact port to the existing File
// ingest/storage/delivery authority. It owns no storage, table, upload state or
// alternate signed-URL format.
type TranslationXLIFFFiles struct {
	files TranslationXLIFFRuntime
}

func NewTranslationXLIFFFiles(files TranslationXLIFFRuntime) (*TranslationXLIFFFiles, error) {
	if translationXLIFFRuntimeIsNil(files) {
		return nil, fmt.Errorf("translation XLIFF File runtime is required")
	}
	return &TranslationXLIFFFiles{files: files}, nil
}

func translationXLIFFRuntimeIsNil(files TranslationXLIFFRuntime) bool {
	if files == nil {
		return true
	}
	value := reflect.ValueOf(files)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (a *TranslationXLIFFFiles) CreateTranslationXLIFF(
	ctx context.Context,
	artifact translationapp.TranslationXLIFFArtifact,
) (*mediaauth.Reference, error) {
	return a.files.CreateGeneratedGeneralFile(ctx, filemediadomain.GeneratedGeneralFileInput{
		FileName: artifact.FileName, Extension: "xlf", MimeType: artifact.MimeType,
		Body: append([]byte(nil), artifact.Body...),
	})
}

func (a *TranslationXLIFFFiles) ReadVerifiedTranslationXLIFF(
	ctx context.Context,
	fileID string,
	maximumBytes int64,
) (translationapp.VerifiedTranslationXLIFF, error) {
	file, err := a.files.ReadVerifiedFileBody(ctx, fileID, maximumBytes)
	if err != nil {
		return translationapp.VerifiedTranslationXLIFF{}, err
	}
	return translationapp.VerifiedTranslationXLIFF{
		FileID: file.FileID, Extension: file.Extension, MimeType: file.MimeType,
		Body: append([]byte(nil), file.Body...),
	}, nil
}

var _ translationapp.TranslationXLIFFFiles = (*TranslationXLIFFFiles)(nil)
