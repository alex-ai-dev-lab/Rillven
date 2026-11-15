package openaicompat

import (
	"errors"
	"fmt"
)

// UnsupportedProtocolConversionError distinguishes a valid source feature
// without a target representation from a malformed request.
type UnsupportedProtocolConversionError struct {
	err error
}

func (e *UnsupportedProtocolConversionError) Error() string { return e.err.Error() }
func (e *UnsupportedProtocolConversionError) Unwrap() error { return e.err }

func unsupportedProtocolConversion(format string, args ...any) error {
	return &UnsupportedProtocolConversionError{err: fmt.Errorf(format, args...)}
}

// UnsupportedProtocolFeature is for valid features a destination cannot express.
// Parsing/shape errors must remain ordinary errors and must not trigger failover.
func UnsupportedProtocolFeature(format string, args ...any) error {
	return unsupportedProtocolConversion(format, args...)
}

func IsUnsupportedProtocolConversion(err error) bool {
	var unsupported *UnsupportedProtocolConversionError
	return errors.As(err, &unsupported)
}
