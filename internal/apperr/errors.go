package apperr

import (
	"errors"
	"fmt"
)

// UserError exposes a concise user-facing message while preserving the full
// wrapped error for logging and debugging.
type UserError interface {
	error
	DisplayMessage() string
}

type Error struct {
	Display string
	Err     error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return e.Display
	}
	return e.Display + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *Error) DisplayMessage() string {
	if e == nil {
		return ""
	}
	return e.Display
}

func New(display string) error {
	return &Error{Display: display}
}

func Newf(format string, args ...any) error {
	return &Error{Display: fmt.Sprintf(format, args...)}
}

func Wrap(display string, err error) error {
	return &Error{Display: display, Err: err}
}

func Format(err error, fallbackPrefix string) string {
	if err == nil {
		return ""
	}
	var display UserError
	if errors.As(err, &display) {
		return display.DisplayMessage()
	}
	if fallbackPrefix == "" {
		return err.Error()
	}
	return fallbackPrefix + ": " + err.Error()
}
