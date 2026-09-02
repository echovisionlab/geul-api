package application

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestTranslationXLIFFFileIDIsStableAcrossJobCorrelation(t *testing.T) {
	t.Parallel()

	job := &model.TranslationJob{
		ID: uuid.NewString(), OperationID: uuid.NewString(), EntityType: "post",
		EntityID: uuid.NewString(), SourceLocale: "ko", TargetLocale: "en",
	}
	first := translationXLIFFFileID(job)
	assert.Contains(t, first, "source:")
	assert.Equal(t, first, translationXLIFFFileID(job))

	job.ID = uuid.NewString()
	job.OperationID = uuid.NewString()
	assert.Equal(t, first, translationXLIFFFileID(job))
}

func TestTranslationXLIFFFileIDTracksDocumentTarget(t *testing.T) {
	t.Parallel()

	job := &model.TranslationJob{
		EntityType: "series", EntityID: uuid.NewString(), SourceLocale: "en", TargetLocale: "fr",
	}
	first := translationXLIFFFileID(job)
	job.TargetLocale = "de"
	assert.NotEqual(t, first, translationXLIFFFileID(job))
}
