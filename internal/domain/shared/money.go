package shared

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/shopspring/decimal"
)

const maxMoneyInteger = "999999999999999999"

var decimalMoneyPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,2})?$`)

// Money is an immutable exact decimal amount and ISO-style currency code.
// Values parsed from external data are non-negative and have at most two places.
type Money struct {
	amount   decimal.Decimal
	currency string
}

func NewMoney(value, currency string) (Money, error) {
	currency = strings.ToUpper(currency)
	if len(currency) != 3 || currency[0] < 'A' || currency[0] > 'Z' || currency[1] < 'A' || currency[1] > 'Z' || currency[2] < 'A' || currency[2] > 'Z' {
		return Money{}, Error("money.invalid_currency", ErrInvalidMoney)
	}
	if !decimalMoneyPattern.MatchString(value) {
		return Money{}, Error("money.invalid_format", ErrInvalidMoney)
	}
	d, err := decimal.NewFromString(value)
	if err != nil || !validMagnitude(d) {
		return Money{}, Error("money.out_of_range", ErrInvalidMoney)
	}
	return Money{amount: d, currency: currency}, nil
}

func ZeroMoney(currency string) (Money, error) { return NewMoney("0", currency) }

func (m Money) Amount() decimal.Decimal { return m.amount }
func (m Money) Currency() string        { return m.currency }
func (m Money) String() string          { return m.amount.StringFixed(2) }
func (m Money) IsZero() bool            { return m.amount.IsZero() }
func (m Money) IsPositive() bool        { return m.amount.IsPositive() }
func (m Money) IsNegative() bool        { return m.amount.IsNegative() }

func (m Money) Add(other Money) (Money, error) {
	if err := m.sameCurrency(other); err != nil {
		return Money{}, err
	}
	d := m.amount.Add(other.amount)
	if !validResult(d) {
		return Money{}, Error("money.out_of_range", ErrInvalidMoney)
	}
	return Money{amount: d, currency: m.currency}, nil
}

func (m Money) Subtract(other Money) (Money, error) {
	if err := m.sameCurrency(other); err != nil {
		return Money{}, err
	}
	d := m.amount.Sub(other.amount)
	if !validResult(d) {
		return Money{}, Error("money.out_of_range", ErrInvalidMoney)
	}
	return Money{amount: d, currency: m.currency}, nil
}

func (m Money) Compare(other Money) (int, error) {
	if err := m.sameCurrency(other); err != nil {
		return 0, err
	}
	return m.amount.Cmp(other.amount), nil
}

func (m Money) sameCurrency(other Money) error {
	if m.currency == "" || other.currency == "" || m.currency != other.currency {
		return Error(fmt.Sprintf("money.currency_mismatch.%s.%s", m.currency, other.currency), ErrCurrencyMismatch)
	}
	return nil
}

func validResult(d decimal.Decimal) bool {
	if d.Exponent() < -2 {
		return false
	}
	return validMagnitude(d)
}

func validMagnitude(d decimal.Decimal) bool {
	if d.Abs().GreaterThanOrEqual(decimal.RequireFromString("1000000000000000000")) {
		return false
	}
	// Decimal's normalized exponent can be below -2 for values ending in zero;
	// their represented numeric scale is still acceptable.
	return true
}
