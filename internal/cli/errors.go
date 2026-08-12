// Package cli owns the client side of the command surface: verb dispatch,
// flag parsing, output formatting and the exit-code error type. The exit
// codes are fixed for the whole project and skills branch on them
// (ARCHITECTURE.md §11.3): 0 success, 1 failure, 2 usage error, 3 refused by
// a safety check, 4 required context unavailable, 5 coordinator unreachable.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Exit codes, fixed for the whole project (ARCHITECTURE.md §11.3).
const (
	ExitOK          = 0
	ExitFailure     = 1
	ExitUsage       = 2
	ExitRefused     = 3
	ExitUnavailable = 4
	ExitUnreachable = 5
)

// Error is the one error type that crosses the process boundary. It carries
// the exit code, the message and the remedy. Every error that reaches the
// user must carry a remedy — the plan's rule is that every error names the
// command that fixes it (plan.md §3) — so this is structural rather than a
// habit: WriteError treats an empty remedy as a defect.
type Error struct {
	Code   int
	Msg    string
	Remedy string
}

func (e *Error) Error() string { return e.Msg }

// New builds an Error with its exit code, message and remedy.
func New(code int, msg, remedy string) *Error {
	return &Error{Code: code, Msg: msg, Remedy: remedy}
}

// Errorf builds an Error with a formatted message.
func Errorf(code int, remedy, format string, args ...any) *Error {
	return New(code, fmt.Sprintf(format, args...), remedy)
}

// UsageError builds an exit-2 error.
func UsageError(remedy, format string, args ...any) *Error {
	return Errorf(ExitUsage, remedy, format, args...)
}

// WriteError prints one error to the diagnostics stream, message and remedy.
// An error without a remedy is a defect and is reported as one.
// WriteError prints one error to the diagnostics stream, message and remedy.
// An error without a remedy is a defect and is reported as one.
func WriteError(w io.Writer, err error) {
	var ce *Error
	if !errors.As(err, &ce) {
		fmt.Fprintf(w, "internal error: %v\n", err)
		return
	}
	fmt.Fprintf(w, "%s\n", ce.Msg)
	if ce.Remedy == "" {
		fmt.Fprintf(w, "internal defect: this error reached the user without a remedy\n")
		return
	}
	fmt.Fprintf(w, "fix: %s\n", ce.Remedy)
}

// WriteJSON prints exactly one JSON object on stdout and nothing else.
func WriteJSON(w io.Writer, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}
