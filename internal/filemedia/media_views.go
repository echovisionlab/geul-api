package filemedia

import "github.com/echovisionlab/geul-api/internal/filemedia/domain"

type Delivery = domain.Delivery
type HLSReference = domain.HLSReference
type ProcessingStatus = domain.ProcessingStatus
type DerivativeKind = domain.DerivativeKind
type MediaDeliveryResult = domain.MediaDeliveryResult
type CompletedUpload = domain.CompletedUpload
type ImportedFile = domain.ImportedFile
type LibraryFile = domain.LibraryFile
type GeneratedOutput = domain.GeneratedOutput
type FileDetails = domain.FileDetails
type LibraryItem = domain.LibraryItem
type LibraryItemType = domain.LibraryItemType
type FolderPathSegment = domain.FolderPathSegment

const (
	LibraryItemUnspecified         = domain.LibraryItemUnspecified
	LibraryItemFolder              = domain.LibraryItemFolder
	LibraryItemFile                = domain.LibraryItemFile
	ProcessingUnspecified          = domain.ProcessingUnspecified
	ProcessingActive               = domain.ProcessingActive
	ProcessingReady                = domain.ProcessingReady
	ProcessingFailed               = domain.ProcessingFailed
	DerivativeUnspecified          = domain.DerivativeUnspecified
	DerivativeThumbnail            = domain.DerivativeThumbnail
	DerivativeHLS                  = domain.DerivativeHLS
	DerivativeSpectrogram          = domain.DerivativeSpectrogram
	DerivativeWaveform             = domain.DerivativeWaveform
	DerivativeFaviconICO           = domain.DerivativeFaviconICO
	DerivativeFaviconPNG16         = domain.DerivativeFaviconPNG16
	DerivativeFaviconPNG32         = domain.DerivativeFaviconPNG32
	DerivativeFaviconPNG48         = domain.DerivativeFaviconPNG48
	DerivativeFaviconAppleTouch180 = domain.DerivativeFaviconAppleTouch180
	DerivativeFaviconManifest192   = domain.DerivativeFaviconManifest192
	DerivativeFaviconManifest512   = domain.DerivativeFaviconManifest512
	DerivativeOptimizedMesh        = domain.DerivativeOptimizedMesh
)

type ScopedContentMediaItem = domain.ScopedContentMediaItem
