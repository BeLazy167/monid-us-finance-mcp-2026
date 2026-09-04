package providers

import "fmt"

// Error types shared by every provider. The service layer maps these to
// Financial Datasets error responses, so classification lives in the type
// rather than in a message prefix.

// InputError marks a request that is invalid on its face. It must be raised
// before any paid provider call, so a bad request costs the caller nothing.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

func newInputErrorf(format string, args ...any) error {
	return &InputError{Msg: fmt.Sprintf(format, args...)}
}

// SchemaDriftError marks a provider payload that no longer matches a shape we
// validated. Surfacing it beats guessing: a wrong number is worse than an
// honest failure.
type SchemaDriftError struct{ Msg string }

func (e *SchemaDriftError) Error() string { return e.Msg }

func schemaDriftf(format string, args ...any) error {
	return &SchemaDriftError{Msg: fmt.Sprintf(format, args...)}
}

// UnsupportedError marks a request that is well formed but that our providers
// cannot honestly answer, such as as_reported statements. It is returned at
// zero cost instead of a fabricated or silently degraded result.
type UnsupportedError struct{ Msg string }

func (e *UnsupportedError) Error() string { return e.Msg }

func unsupportedf(format string, args ...any) error {
	return &UnsupportedError{Msg: fmt.Sprintf(format, args...)}
}
