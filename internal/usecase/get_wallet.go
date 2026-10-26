package usecase

import (
	"context"

	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/repository"
)

type GetWalletOutput struct {
	ID       uuid.UUID   `json:"id"`
	PlayerID uuid.UUID   `json:"playerId"`
	Balance  money.Money `json:"balance"`
	Version  int64       `json:"version"`
}

type GetWalletUseCase struct {
	walletRepo repository.WalletRepository
}

func NewGetWalletUseCase(walletRepo repository.WalletRepository) *GetWalletUseCase {
	return &GetWalletUseCase{walletRepo: walletRepo}
}

func (uc *GetWalletUseCase) Execute(ctx context.Context, walletID uuid.UUID) (*GetWalletOutput, error) {
	w, err := uc.walletRepo.GetByID(ctx, walletID)
	if err != nil {
		return nil, err
	}
	return &GetWalletOutput{
		ID:       w.ID(),
		PlayerID: w.PlayerID(),
		Balance:  w.Balance(),
		Version:  w.Version(),
	}, nil
}
