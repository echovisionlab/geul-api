package ffmpeg

import "time"

// CalculateTimeout calculates a dynamic timeout based on media duration.
// The timeout is estimated as 3x the media duration (to account for processing time),
// clamped between the base minimum and 5x the base minimum.
//
// For example, with a base of 5 minutes:
//   - A 1-minute file gets 5 minutes (minimum)
//   - A 10-minute file gets 30 minutes (10 * 3 = 30)
//   - A 60-minute file gets 25 minutes (5 * 5 = 25, capped at maximum)
func CalculateTimeout(durationSeconds float64, baseMinutes int) time.Duration {
	// Base timeout is the minimum
	minimum := time.Duration(baseMinutes) * time.Minute
	// Maximum is 5x the base
	maximum := minimum * 5

	// Estimate processing time as 3x media duration
	// This accounts for downloading, transcoding (typically 1-2x realtime), and uploading
	estimated := time.Duration(durationSeconds*3) * time.Second

	if estimated < minimum {
		return minimum
	}
	if estimated > maximum {
		return maximum
	}
	return estimated
}
