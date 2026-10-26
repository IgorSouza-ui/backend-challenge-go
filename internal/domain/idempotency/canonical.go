package idempotency

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var ErrInvalidPayload = errors.New("cannot compute canonical hash on empty payload")

// CanonicalPayload represents the normalized business fields of a wager transaction.
// Transport metadata and the Idempotency-Key header itself are strictly excluded.
type CanonicalPayload struct {
	ProviderId                     string         `json:"providerId"`
	ExternalTransactionId          string         `json:"externalTransactionId"`
	PlayerId                       string         `json:"playerId"`
	WalletId                       string         `json:"walletId"`
	RoundId                        string         `json:"roundId"`
	GameId                         string         `json:"gameId"`
	Kind                           string         `json:"kind"`
	Money                          CanonicalMoney `json:"money"`
	ReferenceExternalTransactionId *string        `json:"referenceExternalTransactionId,omitempty"`
}

type CanonicalMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// ComputeHash normalizes the business fields into sorted, canonical JSON and computes SHA-256.
func ComputeHash(payload CanonicalPayload) (string, error) {
	// Trim and normalize strings
	payload.ProviderId = strings.TrimSpace(payload.ProviderId)
	payload.ExternalTransactionId = strings.TrimSpace(payload.ExternalTransactionId)
	payload.PlayerId = strings.TrimSpace(payload.PlayerId)
	payload.WalletId = strings.TrimSpace(payload.WalletId)
	payload.RoundId = strings.TrimSpace(payload.RoundId)
	payload.GameId = strings.TrimSpace(payload.GameId)
	payload.Kind = strings.ToUpper(strings.TrimSpace(payload.Kind))
	payload.Money.Amount = strings.TrimSpace(payload.Money.Amount)
	payload.Money.Currency = strings.ToUpper(strings.TrimSpace(payload.Money.Currency))

	if payload.ReferenceExternalTransactionId != nil {
		trimmed := strings.TrimSpace(*payload.ReferenceExternalTransactionId)
		if trimmed == "" {
			payload.ReferenceExternalTransactionId = nil
		} else {
			payload.ReferenceExternalTransactionId = &trimmed
		}
	}

	// Build raw map to sort keys recursively
	m := map[string]interface{}{
		"providerId":            payload.ProviderId,
		"externalTransactionId": payload.ExternalTransactionId,
		"playerId":              payload.PlayerId,
		"walletId":              payload.WalletId,
		"roundId":               payload.RoundId,
		"gameId":                payload.GameId,
		"kind":                  payload.Kind,
		"money": map[string]interface{}{
			"amount":   payload.Money.Amount,
			"currency": payload.Money.Currency,
		},
	}
	if payload.ReferenceExternalTransactionId != nil {
		m["referenceExternalTransactionId"] = *payload.ReferenceExternalTransactionId
	}

	canonicalJSON, err := marshalSorted(m)
	if err != nil {
		return "", fmt.Errorf("canonical json marshal error: %w", err)
	}

	h := sha256.Sum256(canonicalJSON)
	return hex.EncodeToString(h[:]), nil
}

func marshalSorted(v interface{}) ([]byte, error) {
	switch val := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		var sb strings.Builder
		sb.WriteString("{")
		for i, k := range keys {
			if i > 0 {
				sb.WriteString(",")
			}
			keyBytes, _ := json.Marshal(k)
			sb.Write(keyBytes)
			sb.WriteString(":")
			childBytes, err := marshalSorted(val[k])
			if err != nil {
				return nil, err
			}
			sb.Write(childBytes)
		}
		sb.WriteString("}")
		return []byte(sb.String()), nil

	case []interface{}:
		var sb strings.Builder
		sb.WriteString("[")
		for i, item := range val {
			if i > 0 {
				sb.WriteString(",")
			}
			itemBytes, err := marshalSorted(item)
			if err != nil {
				return nil, err
			}
			sb.Write(itemBytes)
		}
		sb.WriteString("]")
		return []byte(sb.String()), nil

	default:
		return json.Marshal(v)
	}
}
