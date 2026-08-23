package ports

import "errors"

type ErrorKind string

const (
	ErrorKindConfiguration  ErrorKind = "configuration"
	ErrorKindUnavailable    ErrorKind = "unavailable"
	ErrorKindAuthentication ErrorKind = "authentication"
	ErrorKindRateLimit      ErrorKind = "rate_limit"
	ErrorKindProvider       ErrorKind = "provider"
	ErrorKindTransport      ErrorKind = "transport"
	ErrorKindTimeout        ErrorKind = "timeout"
	ErrorKindInvalidInput   ErrorKind = "invalid_input"
	ErrorKindProtocol       ErrorKind = "protocol"
)

type RunError struct {
	Kind      ErrorKind
	Reason    string
	Retryable bool
	Cause     error
}

func (e *RunError) Error() string {
	if e == nil || e.Reason == "" {
		return "provider execution failed"
	}
	return e.Reason
}

func (e *RunError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func NewRunError(kind ErrorKind, reason string, retryable bool, cause error) *RunError {
	return &RunError{Kind: kind, Reason: reason, Retryable: retryable, Cause: cause}
}

func FailureDetails(err error) (reason string, retryable bool) {
	var runErr *RunError
	if errors.As(err, &runErr) {
		return runErr.Error(), runErr.Retryable
	}
	return "provider execution failed", false
}
