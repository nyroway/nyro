package ir

// MediaSource is the data source for an image or audio content block.
// Sealed union; dispatch via type switch. Ported from MediaSource.
type MediaSource interface{ mediaSource() }

// Base64Media is inline base64-encoded data.
type Base64Media struct {
	MediaType string
	Data      string
}

func (*Base64Media) mediaSource() {}

// URLMedia references media by URL.
type URLMedia struct{ URL string }

func (*URLMedia) mediaSource() {}

// FileIDMedia references a provider-side file.
type FileIDMedia struct {
	FileID string
	Detail string // optional; empty = absent
}

func (*FileIDMedia) mediaSource() {}

// DocumentSource is the source of a document content block (Anthropic).
// Sealed union. Ported from DocumentSource.
type DocumentSource interface{ documentSource() }

// Base64PdfDoc is an inline base64-encoded PDF.
type Base64PdfDoc struct{ Data string }

func (*Base64PdfDoc) documentSource() {}

// PlainTextDoc is inline plain text.
type PlainTextDoc struct{ Data string }

func (*PlainTextDoc) documentSource() {}

// URLDoc references a document by URL.
type URLDoc struct{ URL string }

func (*URLDoc) documentSource() {}

// BlocksDoc is document content already stored as content blocks.
type BlocksDoc struct{ Blocks []ContentBlock }

func (*BlocksDoc) documentSource() {}
