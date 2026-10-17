package application

import (
	"github.com/junglegaming/backend-challenge-go/internal/domain/wagering"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

func wagerEventData(t *wagering.WagerTransaction) map[string]any {
	data := map[string]any{
		"transactionId": t.ID(), "source": "EXTERNAL", "providerId": t.ProviderID(),
		"externalTransactionId": t.ExternalID(), "playerId": t.PlayerID(), "walletId": t.WalletID(),
		"roundId": t.RoundID(), "gameId": t.GameID(), "kind": t.Kind(), "status": t.Status(),
		"money":     map[string]string{"amount": t.Amount().String(), "currency": t.Amount().Currency()},
		"createdAt": t.CreatedAt().UTC(), "updatedAt": t.UpdatedAt().UTC(),
	}
	if t.Kind() == wagering.Opening {
		data["source"] = "INTERNAL"
	}
	if t.ReferenceExternalID() != "" {
		data["referenceExternalTransactionId"] = t.ReferenceExternalID()
	}
	if t.FailureCode() != "" {
		data["failureCode"] = t.FailureCode()
	}
	if balance := t.ResultBalance(); balance != nil {
		data["balance"] = map[string]string{"amount": balance.String(), "currency": balance.Currency()}
	}
	return data
}

func walletBalanceEventData(entry *wallet.LedgerEntry, version int64) map[string]any {
	return map[string]any{
		"walletId": entry.WalletID(), "transactionId": entry.TransactionID(), "direction": entry.Direction(),
		"money":         map[string]string{"amount": entry.Amount().String(), "currency": entry.Amount().Currency()},
		"balanceBefore": map[string]string{"amount": entry.BalanceBefore().String(), "currency": entry.BalanceBefore().Currency()},
		"balanceAfter":  map[string]string{"amount": entry.BalanceAfter().String(), "currency": entry.BalanceAfter().Currency()},
		"walletVersion": version,
	}
}
