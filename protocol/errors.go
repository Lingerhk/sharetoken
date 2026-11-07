package protocol

import (
	"errors"
	"fmt"
)

// Error codes carried in error frames, so a client can tell "try another node"
// (busy, upstream, bad_ticket) from "stop" (insufficient funds, bad request).
const (
	CodeBadRequest        = "bad_request"
	CodeBadTicket         = "bad_ticket"
	CodeBusy              = "busy"
	CodeBudget            = "budget"
	CodeNoOffers          = "no_offers"
	CodeInsufficientFunds = "insufficient_funds"
	CodeUpstream          = "upstream"
	CodeForbidden         = "forbidden"
	CodeNotFound          = "not_found"
	CodeConflict          = "conflict"
	CodeInternal          = "internal"
)

// Error is a coded protocol error. Local logic returns it, and it travels
// verbatim in error frames.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf builds a coded error.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf returns the code of err if it is or wraps an *Error, else "".
func CodeOf(err error) string {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}
