package mutation

import (
	"context"
	"testing"
)

func TestAfterCommitRunsNowWithoutASharedTransaction(t *testing.T) {
	ran := false
	AfterCommit(context.Background(), func() { ran = true })
	if !ran {
		t.Fatal("with no shared transaction the mutation has committed, so the work must run now")
	}
}

// A context kept past the end of its transaction must not reach a closed handle.
func TestFinishedSharedTransactionIsNotJoined(t *testing.T) {
	shared := &sharedTransaction{done: true}
	ctx := context.WithValue(context.Background(), sharedTransactionKey{}, shared)
	if joinedTransaction(ctx) != nil {
		t.Fatal("a finished transaction was joined")
	}
	ran := false
	AfterCommit(ctx, func() { ran = true })
	if !ran {
		t.Fatal("after the transaction ended the work must run now")
	}
}
