package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wagering"
)

// CanonicalPayloadHash hashes normalized business fields, independent of the
// transport envelope and idempotency key so HTTP and SQS share replay identity.
func CanonicalPayloadHash(providerID string, in SubmitWagerInput) (string, error) {
	canonical := struct {
		ProviderID            string        `json:"providerId"`
		ExternalTransactionID string        `json:"externalTransactionId"`
		PlayerID              string        `json:"playerId"`
		WalletID              string        `json:"walletId"`
		RoundID               string        `json:"roundId"`
		GameID                string        `json:"gameId"`
		Kind                  wagering.Kind `json:"kind"`
		Money                 struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		} `json:"money"`
		ReferenceExternalTransactionID string `json:"referenceExternalTransactionId,omitempty"`
	}{ProviderID: providerID, ExternalTransactionID: in.ExternalID, PlayerID: in.PlayerID, WalletID: in.WalletID, RoundID: in.RoundID, GameID: in.GameID, Kind: in.Kind, ReferenceExternalTransactionID: in.ReferenceExternalID}
	canonical.Money.Amount = in.Amount.String()
	canonical.Money.Currency = strings.ToUpper(strings.TrimSpace(in.Amount.Currency()))
	body, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}
