package shared

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidMoney        = errors.New("invalid money")
	ErrCurrencyMismatch    = errors.New("currency mismatch")
	ErrInvalidArgument     = errors.New("invalid argument")
	ErrInsufficientFunds   = errors.New("insufficient funds")
	ErrInvalidTransition   = errors.New("invalid transaction transition")
	ErrInvalidOperation    = errors.New("invalid wager operation")
	ErrInvalidReference    = errors.New("invalid wager reference")
	ErrDuplicateReversal   = errors.New("duplicate reversal")
	ErrTerminalTransaction = errors.New("transaction is terminal")
)

// DomainError gives a stable code and cause while keeping transport details out
// of the domain. Callers may classify it with errors.Is/As.
type DomainError struct {
	Code  string
	Cause error
}

func (e *DomainError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause == nil {
		return e.Code
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Cause)
}

func (e *DomainError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *DomainError) Is(target error) bool {
	return e != nil && (target == e.Cause || errors.Is(e.Cause, target))
}

func Error(code string, cause error) error { return &DomainError{Code: code, Cause: cause} }
