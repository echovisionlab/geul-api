package application

import (
	"github.com/echovisionlab/geul-api/internal/translation"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildDefaultTranslationGenerationProfileUsesEditorialPlainStyle(t *testing.T) {
	t.Parallel()

	profile := buildDefaultTranslationGenerationProfile("post", "en", "ko", false, nil)

	assert.Equal(t, translation.ContentKindEditorial, profile.ContentKind)
	assert.Equal(t, translation.RegisterNeutralPlain, profile.TargetRegister)
	assert.Equal(t, translation.RegisterPolicyTargetDefault, profile.RegisterPolicy)
	assert.Contains(t, profile.StyleInstructions, "Use neutral written Korean plain style ending in -다 or -한다; do not mix in polite -습니다 or -요 endings unless the content is direct user guidance.")
}

func TestBuildDefaultTranslationGenerationProfilePreservesKoreanSourceRegister(t *testing.T) {
	t.Parallel()

	profile := buildDefaultTranslationGenerationProfile("page", "ko", "ja", false, nil)

	assert.Equal(t, translation.RegisterPolicyPreserveSourceWhenClear, profile.RegisterPolicy)
	assert.Contains(t, profile.StyleInstructions, "If the source language has a clear honorific or plain register, preserve that register instead of forcing a different target register.")
}

func TestBuildDefaultTranslationGenerationProfileUsesPoliteGuidanceStyle(t *testing.T) {
	t.Parallel()

	profile := buildDefaultTranslationGenerationProfile("email_template", "en", "ja", true, nil)

	assert.Equal(t, translation.ContentKindDirectUserGuidance, profile.ContentKind)
	assert.Equal(t, translation.RegisterPolite, profile.TargetRegister)
	assert.Contains(t, profile.StyleInstructions, "Use consistent polite Japanese appropriate for direct user guidance, using natural desu/masu style.")
}
