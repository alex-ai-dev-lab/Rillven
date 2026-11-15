package types

import (
	"errors"
	"net/http"
)

const (
	ErrorCodeConversationContextUnavailable ErrorCode = "response_context_unavailable"
	ErrorCodeConversationStoreUnavailable   ErrorCode = "response_context_backend_unavailable"
	ErrorCodeBillingCommitUnavailable       ErrorCode = "billing_error"
	ErrorCodeResponseBufferUnavailable      ErrorCode = "response_buffer_unavailable"
	ErrorCodeUpstreamAdmissionUnavailable   ErrorCode = "upstream_admission_unavailable"
)

func NewResponseBufferUnavailable() *NewAPIError {
	return NewErrorWithStatusCode(errors.New("The gateway response buffer is at capacity. Retry later."), ErrorCodeResponseBufferUnavailable, http.StatusServiceUnavailable, ErrOptionWithSkipRetry())
}

func IsGatewayCoordinationError(err *NewAPIError) bool {
	if err == nil || err.GetErrorType() != ErrorTypeNewAPIError {
		return false
	}
	switch err.GetErrorCode() {
	case ErrorCodeUpstreamAdmissionUnavailable, ErrorCodeResponseBufferUnavailable, ErrorCodeBillingCommitUnavailable, ErrorCodeConversationContextUnavailable, ErrorCodeConversationStoreUnavailable:
		return true
	}
	return false
}

func NewConversationContextUnavailable() *NewAPIError {
	return NewErrorWithStatusCode(errors.New("The previous response context cannot be recovered for this request."), ErrorCodeConversationContextUnavailable, http.StatusConflict, ErrOptionWithSkipRetry())
}

func NewBillingCommitUnavailable() *NewAPIError {
	return NewErrorWithStatusCode(errors.New("The response is awaiting billing settlement. Please retry later."), ErrorCodeBillingCommitUnavailable, http.StatusServiceUnavailable, ErrOptionWithSkipRetry())
}

func NewConversationStoreUnavailable() *NewAPIError {
	return NewErrorWithStatusCode(errors.New("Response context storage is temporarily unavailable. Retry later."), ErrorCodeConversationStoreUnavailable, http.StatusServiceUnavailable, ErrOptionWithSkipRetry())
}
