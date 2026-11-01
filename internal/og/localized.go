package og

type LocalizedGenerationDisposition int

const (
	LocalizedGenerationMissing LocalizedGenerationDisposition = iota
	LocalizedGenerationPending
	LocalizedGenerationReady
	LocalizedGenerationTerminal
)

// ClassifyLocalizedGeneration classifies the status of a locale target's
// latest generation.
func ClassifyLocalizedGeneration(status GenerationStatus) LocalizedGenerationDisposition {
	switch status {
	case StatusQueued, StatusProcessing:
		return LocalizedGenerationPending
	case StatusReady:
		return LocalizedGenerationReady
	default:
		return LocalizedGenerationTerminal
	}
}
