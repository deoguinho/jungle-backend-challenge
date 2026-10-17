package shared

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestNewMoneyAndValidation(t *testing.T) {
	for _, input := range []string{"", "-1.00", "+1", "1e2", "1.001", "NaN", "Infinity", " 1.00"} {
		if _, err := NewMoney(input, "USD"); !errors.Is(err, ErrInvalidMoney) {
			t.Errorf("NewMoney(%q) error = %v", input, err)
		}
	}
	for _, currency := range []string{"", "US", "US1", "USDD"} {
		if _, err := NewMoney("1.00", currency); !errors.Is(err, ErrInvalidMoney) {
			t.Errorf("currency %q: %v", currency, err)
		}
	}
	for _, input := range []string{"0", "1", "1.0", "999999999999999999.99"} {
		m, err := NewMoney(input, "usd")
		if err != nil {
			t.Fatalf("NewMoney(%q): %v", input, err)
		}
		if m.Currency() != "USD" {
			t.Fatalf("currency = %s", m.Currency())
		}
	}
	if _, err := NewMoney("1000000000000000000", "USD"); !errors.Is(err, ErrInvalidMoney) {
		t.Fatalf("expected out of range, got %v", err)
	}
}

func TestMoneyArithmeticExactAndCurrencySafe(t *testing.T) {
	a, _ := NewMoney("0.10", "USD")
	b, _ := NewMoney("0.20", "USD")
	sum, err := a.Add(b)
	if err != nil || sum.String() != "0.30" {
		t.Fatalf("sum = %s, %v", sum.String(), err)
	}
	diff, err := a.Subtract(b)
	if err != nil || diff.String() != "-0.10" {
		t.Fatalf("difference = %s, %v", diff.String(), err)
	}
	cad, _ := NewMoney("0.20", "CAD")
	if _, err = a.Add(cad); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("expected currency mismatch, got %v", err)
	}
	if _, err = a.Compare(cad); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("expected currency mismatch, got %v", err)
	}
	if !sum.IsPositive() || sum.IsZero() || !diff.IsNegative() {
		t.Fatal("unexpected sign predicates")
	}
	if cmp, err := b.Compare(a); err != nil || cmp <= 0 {
		t.Fatalf("compare result=%d err=%v", cmp, err)
	}
	if cmp, err := a.Compare(a); err != nil || cmp != 0 {
		t.Fatalf("equal compare result=%d err=%v", cmp, err)
	}
	if a.Amount().String() != "0.1" {
		t.Fatalf("exact decimal amount = %s", a.Amount())
	}
	max, _ := NewMoney("999999999999999999.99", "USD")
	if _, err := max.Add(mustMoneyForTest("0.01", "USD")); !errors.Is(err, ErrInvalidMoney) {
		t.Fatalf("expected overflow, got %v", err)
	}
	if validResult(decimal.RequireFromString("0.001")) {
		t.Fatal("unexpectedly accepted sub-cent result")
	}
	if !validResult(decimal.RequireFromString("0.01")) {
		t.Fatal("valid cent result rejected")
	}
}

func TestDomainErrorSupportsIsAndAs(t *testing.T) {
	err := Error("wallet.low_balance", ErrInsufficientFunds)
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatal("errors.Is did not match wrapped sentinel")
	}
	var typed *DomainError
	if !errors.As(err, &typed) || typed.Code != "wallet.low_balance" {
		t.Fatalf("errors.As = %#v", typed)
	}
	if got := typed.Error(); got == "" || typed.Unwrap() != ErrInsufficientFunds {
		t.Fatalf("typed error string/unwrap: %q", got)
	}
	if got := Error("context", nil).(*DomainError).Error(); got != "context" {
		t.Fatalf("error without cause = %q", got)
	}
	var nilError *DomainError
	if nilError.Error() != "" || nilError.Unwrap() != nil || nilError.Is(ErrInvalidArgument) {
		t.Fatal("nil DomainError behavior is incorrect")
	}
}

func mustMoneyForTest(v, c string) Money { m, _ := NewMoney(v, c); return m }
