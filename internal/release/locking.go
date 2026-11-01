package release

import "context"

// lockReleaseRow is the Lock of a Release mutation: the root row FOR UPDATE,
// answering not found before authorization when it is missing. into, when
// set, receives the locked row.
func lockReleaseRow(releaseID string, into *Release) func(context.Context, Store) error {
	return func(ctx context.Context, store Store) error {
		locked, err := store.LockRelease(ctx, releaseID, UpdateLock)
		if err != nil {
			return releaseMissingAs(err, releaseNotFound(releaseID))
		}
		if into != nil {
			*into = locked
		}
		return nil
	}
}

// lockTrackRow locks the Track FOR UPDATE, answering not found before
// authorization when it is missing. into, when set, receives the row.
func lockTrackRow(ctx context.Context, store Store, trackID string, into *Track) error {
	locked, err := store.LockTrack(ctx, trackID)
	if err != nil {
		return missingAs(err, ErrTrackNotFound, trackNotFound())
	}
	if into != nil {
		*into = locked
	}
	return nil
}
