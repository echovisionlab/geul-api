package mcp

import (
	"fmt"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
)

// BenchmarkFocusedAccepted uses fixed application results and includes the
// text/structured response boundary exercised by applyRequest.
func BenchmarkFocusedAccepted(b *testing.B) {
	for _, count := range []int{1, 32} {
		for _, created := range []bool{false, true} {
			b.Run(fmt.Sprintf("changes=%d/created=%t", count, created), func(b *testing.B) {
				targetRevision := core.Revision("target-revision-42")
				result := core.ApplyResult{DocumentRevision: "document-revision-42", TargetRevision: &targetRevision}
				for index := 0; index < count; index++ {
					result.Changes = append(result.Changes, core.Change{
						Operation: index, Kind: core.OperationInsertBlock,
						AffectedHandles: []string{fmt.Sprintf("paragraph-%02d", index), "rich-text-section"},
					})
				}
				var blockID core.BlockID
				if created {
					blockID = "55555555-5555-4555-8555-555555555555"
				}
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					encoded, err := encodeFocusedAccepted(result, blockID)
					if err != nil {
						b.Fatal(err)
					}
					response, err := structuredResult(encoded, false)
					if err != nil || len(response.Content) != 1 {
						b.Fatalf("response = %+v, error = %v", response, err)
					}
				}
			})
		}
	}
}
