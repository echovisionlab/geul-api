package filemedia

import (
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/fileref"
)

type Delivery = fileref.Delivery
type HLSReference = fileref.HLSReference
type ProcessingStatus = fileref.ProcessingStatus
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
	ProcessingUnspecified          = fileref.ProcessingUnspecified
	ProcessingActive               = fileref.ProcessingActive
	ProcessingReady                = fileref.ProcessingReady
	ProcessingFailed               = fileref.ProcessingFailed
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
