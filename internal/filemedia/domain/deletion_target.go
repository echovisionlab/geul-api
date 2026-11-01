package domain

import (
	"strings"
	"time"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
)

type ObjectTarget struct {
	FileID    string
	ObjectKey string
	Extension string
	MimeType  string
}
type GenerationTarget struct {
	GenerationID string
	FileID       string
	ObjectPrefix string
}
type DeletionTarget struct {
	FileID          string
	Original        *ObjectTarget
	Generations     []*GenerationTarget
	HasPublicAssets bool
	Timestamp       time.Time
}

func CanonicalObjectTarget(file File) (*ObjectTarget, error) {
	id := strings.TrimSpace(file.ID)
	extension := strings.ToLower(strings.TrimSpace(file.Extension))
	key, err := mediaauth.MediaObjectKey(id, extension)
	if err != nil {
		return nil, err
	}
	return &ObjectTarget{FileID: id, ObjectKey: key, Extension: extension, MimeType: strings.TrimSpace(file.MimeType)}, nil
}
