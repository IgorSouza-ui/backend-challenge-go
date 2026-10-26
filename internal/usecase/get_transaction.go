package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/repository"
	"github.com/junglegaming/backend-challenge-go/internal/domain/transaction"
)

type TransactionDTO struct {
	ID                             uuid.UUID         `json:"id"`
	ProviderID                     string            `json:"providerId"`
	ExternalTransactionID          string            `json:"externalTransactionId"`
	WalletID                       uuid.UUID         `json:"walletId"`
	PlayerID                       uuid.UUID         `json:"playerId"`
	RoundID                        string            `json:"roundId"`
	GameID                         string            `json:"gameId"`
	Kind                           transaction.Kind  `json:"kind"`
	Amount                         money.Money       `json:"amount"`
	ReferenceExternalTransactionID *string           `json:"referenceExternalTransactionId,omitempty"`
	ResolvedInternalReferenceID    *uuid.UUID        `json:"resolvedInternalReferenceId,omitempty"`
	State                          transaction.State `json:"state"`
	FailureCode                    *string           `json:"failureCode,omitempty"`
	BalanceAtProcess               *money.Money      `json:"balanceAtProcess,omitempty"`
	CreatedAt                      string            `json:"createdAt"`
	UpdatedAt                      string            `json:"updatedAt"`
}

type GetTransactionUseCase struct {
	txRepo repository.TransactionRepository
}

func NewGetTransactionUseCase(txRepo repository.TransactionRepository) *GetTransactionUseCase {
	return &GetTransactionUseCase{txRepo: txRepo}
}

func (uc *GetTransactionUseCase) GetByID(ctx context.Context, id uuid.UUID) (*TransactionDTO, error) {
	tx, err := uc.txRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return uc.toDTO(tx), nil
}

func (uc *GetTransactionUseCase) GetByProviderAndExternalID(ctx context.Context, providerID, externalID string) (*TransactionDTO, error) {
	tx, err := uc.txRepo.GetByProviderAndExternalID(ctx, providerID, externalID)
	if err != nil {
		return nil, err
	}
	return uc.toDTO(tx), nil
}

func (uc *GetTransactionUseCase) toDTO(t *transaction.WagerTransaction) *TransactionDTO {
	return &TransactionDTO{
		ID:                             t.ID(),
		ProviderID:                     t.ProviderID(),
		ExternalTransactionID:          t.ExternalTransactionID(),
		WalletID:                       t.WalletID(),
		PlayerID:                       t.PlayerID(),
		RoundID:                        t.RoundID(),
		GameID:                         t.GameID(),
		Kind:                           t.Kind(),
		Amount:                         t.Amount(),
		ReferenceExternalTransactionID: t.ReferenceExternalTransactionID(),
		ResolvedInternalReferenceID:    t.ResolvedInternalReferenceID(),
		State:                          t.State(),
		FailureCode:                    t.FailureCode(),
		BalanceAtProcess:               t.BalanceAtProcess(),
		CreatedAt:                      t.CreatedAt().Format(time.RFC3339),
		UpdatedAt:                      t.UpdatedAt().Format(time.RFC3339),
	}
}
