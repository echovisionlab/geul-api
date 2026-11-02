package ogpostgres

import (
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	optional "github.com/echovisionlab/geul-api/internal/optional"
)

func targetRow(target og.GenerationTarget) model.OgGenerationTarget {
	return model.OgGenerationTarget{
		ID: target.ID, EntityType: target.EntityType, EntityID: target.EntityID, TargetKind: target.TargetKind,
		Locale: target.Locale, LatestGenerationID: target.LatestGenerationID,
		CreatedAt: target.CreatedAt, UpdatedAt: target.UpdatedAt,
	}
}

func targetFromRow(row model.OgGenerationTarget) og.GenerationTarget {
	return og.GenerationTarget{
		ID: row.ID, EntityType: row.EntityType, EntityID: row.EntityID, TargetKind: row.TargetKind,
		Locale: row.Locale, LatestGenerationID: row.LatestGenerationID,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func targetsFromRows(rows []model.OgGenerationTarget) []og.GenerationTarget {
	targets := make([]og.GenerationTarget, len(rows))
	for i, row := range rows {
		targets[i] = targetFromRow(row)
	}
	return targets
}

func generationRow(generation og.Generation) model.OgGeneration {
	return model.OgGeneration{
		ID: generation.ID, RunID: generation.RunID, TargetID: generation.TargetID,
		RequestSequence: generation.RequestSequence, Status: string(generation.Status),
		EntitySnapshot: generation.EntitySnapshot, ProcessingAt: generation.ProcessingAt,
		LeaseToken: generation.LeaseToken, LeaseExpiresAt: generation.LeaseExpiresAt,
		DeadlineAt: generation.DeadlineAt, LastErrorCode: generation.LastErrorCode,
		ReadyAt: generation.ReadyAt, FailedAt: generation.FailedAt, SupersededAt: generation.SupersededAt,
		SupersededByID: generation.SupersededByID, CancelledAt: generation.CancelledAt,
		CompletedAt: generation.CompletedAt, CreatedAt: generation.CreatedAt, UpdatedAt: generation.UpdatedAt,
	}
}

func generationFromRow(row model.OgGeneration) og.Generation {
	return og.Generation{
		ID: row.ID, RunID: row.RunID, TargetID: row.TargetID,
		RequestSequence: row.RequestSequence, Status: og.GenerationStatus(row.Status),
		EntitySnapshot: row.EntitySnapshot, ProcessingAt: row.ProcessingAt,
		LeaseToken: row.LeaseToken, LeaseExpiresAt: row.LeaseExpiresAt,
		DeadlineAt: row.DeadlineAt, LastErrorCode: row.LastErrorCode,
		ReadyAt: row.ReadyAt, FailedAt: row.FailedAt, SupersededAt: row.SupersededAt,
		SupersededByID: row.SupersededByID, CancelledAt: row.CancelledAt,
		CompletedAt: row.CompletedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func assetFromModel(asset model.PublicAsset) og.Asset {
	return og.Asset{
		ID: asset.ID, SourceFileID: asset.SourceFileID, Kind: asset.Kind,
		Extension: asset.Extension, MimeType: asset.MimeType, Disposition: asset.Disposition,
		FileSize: asset.FileSize, SHA256: asset.SHA256, Status: og.AssetStatus(asset.Status),
		ReadyAt: asset.ReadyAt, CreatedAt: asset.CreatedAt,
	}
}

func assetRow(asset og.Asset) model.PublicAsset {
	return model.PublicAsset{
		ID: asset.ID, SourceFileID: asset.SourceFileID, Kind: asset.Kind,
		Extension: asset.Extension, MimeType: asset.MimeType, Disposition: asset.Disposition,
		FileSize: asset.FileSize, SHA256: asset.SHA256, Status: string(asset.Status),
		ReadyAt: asset.ReadyAt, CreatedAt: asset.CreatedAt,
	}
}

func refFromProto(ref *mediaasset.Reference) og.AssetRef {
	return og.AssetRef{
		AssetID: optional.Value(ref).AssetID, URL: optional.Value(ref).URL, Extension: optional.Value(ref).Extension, MimeType: optional.Value(ref).MimeType,
		FileSize: optional.Value(ref).FileSize, SHA256: optional.Value(ref).SHA256,
	}
}
