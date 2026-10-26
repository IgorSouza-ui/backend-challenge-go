package usecase

import (
	"context"
	"encoding/base64"
	"time"

	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/repository"
)

type LedgerEntryDTO struct {
	ID            uuid.UUID        `json:"id"`
	WalletID      uuid.UUID        `json:"walletId"`
	TransactionID uuid.UUID        `json:"transactionId"`
	Direction     ledger.Direction `json:"direction"`
	Amount        money.Money      `json:"amount"`
	BalanceBefore money.Money      `json:"balanceBefore"`
	BalanceAfter  money.Money      `json:"balanceAfter"`
	CreatedAt     string           `json:"createdAt"`
}

type GetLedgerOutput struct {
	Entries    []LedgerEntryDTO `json:"entries"`
	NextCursor *string          `json:"nextCursor,omitempty"`
}

type GetLedgerUseCase struct {
	ledgerRepo repository.LedgerRepository
}

func NewGetLedgerUseCase(ledgerRepo repository.LedgerRepository) *GetLedgerUseCase {
	return &GetLedgerUseCase{ledgerRepo: ledgerRepo}
}

func (uc *GetLedgerUseCase) Execute(ctx context.Context, walletID uuid.UUID, cursorStr *string, limit int) (*GetLedgerOutput, error) {
	var cursorTime *time.Time
	if cursorStr != nil && *cursorStr != "" {
		decoded, err := base64.URLEncoding.DecodeString(*cursorStr)
		if err == nil {
			t, err := time.Parse(time.RFC3339Nano, string(decoded))
			if err == nil {
				cursorTime = &t
			}
		}
	}

	entries, lastTime, err := uc.ledgerRepo.ListByWallet(ctx, walletID, cursorTime, limit)
	if err != nil {
		return nil, err
	}

	dtos := make([]LedgerEntryDTO, 0, len(entries))
	for _, e := range entries {
		dtos = append(dtos, LedgerEntryDTO{
			ID:            e.ID(),
			WalletID:      e.WalletID(),
			TransactionID: e.TransactionID(),
			Direction:     e.Direction(),
			Amount:        e.Amount(),
			BalanceBefore: e.BalanceBefore(),
			BalanceAfter:  e.BalanceAfter(),
			CreatedAt:     e.CreatedAt().Format(time.RFC3339),
		})
	}

	var nextCursor *string
	if lastTime != nil && len(entries) == limit {
		encoded := base64.URLEncoding.EncodeToString([]byte(lastTime.Format(time.RFC3339Nano)))
		nextCursor = &encoded
	}

	return &GetLedgerOutput{
		Entries:    dtos,
		NextCursor: nextCursor,
	}, nil
}
