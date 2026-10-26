package usecase

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/repository"
)

type ReconcileWalletInput struct {
	WalletID uuid.UUID
}

type ReconcileWalletOutput struct {
	WalletID          uuid.UUID   `json:"walletId"`
	StoredBalance     money.Money `json:"storedBalance"`
	CalculatedBalance money.Money `json:"calculatedBalance"`
	Difference        money.Money `json:"difference"`
	Consistent        bool        `json:"consistent"`
	CheckedEntries    int         `json:"checkedEntries"`
}

type ReconcileWalletUseCase struct {
	walletRepo repository.WalletRepository
	ledgerRepo repository.LedgerRepository
}

func NewReconcileWalletUseCase(
	walletRepo repository.WalletRepository,
	ledgerRepo repository.LedgerRepository,
) *ReconcileWalletUseCase {
	return &ReconcileWalletUseCase{
		walletRepo: walletRepo,
		ledgerRepo: ledgerRepo,
	}
}

func (uc *ReconcileWalletUseCase) Execute(ctx context.Context, input ReconcileWalletInput) (*ReconcileWalletOutput, error) {
	w, err := uc.walletRepo.GetByID(ctx, input.WalletID)
	if err != nil {
		return nil, fmt.Errorf("wallet not found: %w", err)
	}

	calcBal, count, err := uc.ledgerRepo.CalculateBalance(ctx, input.WalletID)
	if err != nil {
		return nil, fmt.Errorf("ledger calculation failed: %w", err)
	}

	diff, err := w.Balance().Sub(calcBal)
	if err != nil {
		return nil, fmt.Errorf("difference computation failed: %w", err)
	}

	consistent := diff.IsZero()

	return &ReconcileWalletOutput{
		WalletID:          w.ID(),
		StoredBalance:     w.Balance(),
		CalculatedBalance: calcBal,
		Difference:        diff,
		Consistent:        consistent,
		CheckedEntries:    count,
	}, nil
}
