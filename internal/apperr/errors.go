package apperr

import (
	"context"
	"errors"
	"fmt"
)

type Kind string

const (
	KindUnknown    Kind = ""
	KindCanceled   Kind = "canceled"
	KindConfig     Kind = "config"
	KindPermission Kind = "permission"
	KindProvider   Kind = "provider"
	KindSession    Kind = "session"
	KindToolInput  Kind = "tool_input"
	KindToolExec   Kind = "tool_exec"
)

// UserError exposes a concise user-facing message while preserving the full
// wrapped error for logging and debugging.
type UserError interface {
	error
	DisplayMessage() string
}

type Error struct {
	Display string
	Kind    Kind
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

func (e *Error) ErrorKind() Kind {
	if e == nil {
		return KindUnknown
	}
	return e.Kind
}

func New(display string) error {
	return &Error{Display: display}
}

func NewKind(kind Kind, display string) error {
	return &Error{Display: display, Kind: kind}
}

func Newf(format string, args ...any) error {
	return &Error{Display: fmt.Sprintf(format, args...)}
}

func NewKindf(kind Kind, format string, args ...any) error {
	return &Error{Display: fmt.Sprintf(format, args...), Kind: kind}
}

func Wrap(display string, err error) error {
	return &Error{Display: display, Err: err}
}

func WrapKind(kind Kind, display string, err error) error {
	return &Error{Display: display, Kind: kind, Err: err}
}

func KindOf(err error) Kind {
	original := err
	for err != nil {
		type kindCarrier interface {
			ErrorKind() Kind
		}
		if carrier, ok := err.(kindCarrier); ok && carrier.ErrorKind() != KindUnknown {
			return carrier.ErrorKind()
		}
		err = errors.Unwrap(err)
	}
	if errors.Is(original, context.Canceled) {
		return KindCanceled
	}
	return KindUnknown
}

func IsKind(err error, kind Kind) bool {
	return KindOf(err) == kind
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
