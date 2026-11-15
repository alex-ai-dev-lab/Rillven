package common

// ManagedConversation is the boundary between protocol delivery and durable
// recovery. It observes protocol objects, never arbitrary text replacements.
type ManagedConversation interface {
	ObservationOnly() bool
	ObserveResponse(payload []byte, stream bool) ([]byte, error)
	ValidateResponse() error
	PrepareCommit(quota int, ledgerID uint64) error
	PublishCommit() error
	CommitPrepared() bool
	CommitPublished() bool
	BufferLimits() (request, global int)
}

// Implemented by a staging writer. Settlement calls this before preparing the
// checkpoint, including adapters whose non-stream response arrives in chunks.
type ResponseFinalizer interface{ FinalizeResponse() error }
