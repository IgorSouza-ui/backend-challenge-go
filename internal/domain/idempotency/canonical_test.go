package idempotency_test

import (
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/idempotency"
)

func TestIdempotency_DeterministicHash(t *testing.T) {
	p1 := idempotency.CanonicalPayload{
		ProviderId:            "provider-a",
		ExternalTransactionId: "txn-123",
		PlayerId:              "player-1",
		WalletId:              "wallet-1",
		RoundId:               "round-987",
		GameId:                "game-1",
		Kind:                  "BET",
		Money: idempotency.CanonicalMoney{
			Amount:   "25.00",
			Currency: "BRL",
		},
	}

	hash1, err := idempotency.ComputeHash(p1)
	if err != nil {
		t.Fatalf("ComputeHash failed: %v", err)
	}

	// Repeated computation should be bit-for-bit identical
	hash2, err := idempotency.ComputeHash(p1)
	if err != nil {
		t.Fatalf("ComputeHash 2 failed: %v", err)
	}

	if hash1 != hash2 {
		t.Fatalf("hashes not deterministic: %s != %s", hash1, hash2)
	}

	// Changing one field must change the hash
	p2 := p1
	p2.Money.Amount = "30.00"
	hash3, _ := idempotency.ComputeHash(p2)
	if hash1 == hash3 {
		t.Fatalf("different payloads produced same hash")
	}
}
