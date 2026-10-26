package tests

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/junglegaming/backend-challenge-go/internal/domain/events"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/repository"
	"github.com/junglegaming/backend-challenge-go/internal/domain/transaction"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

type MockTx struct {
	pgx.Tx
	mu        sync.Mutex
	unlockFns []func()
	closed    bool
}

func (m *MockTx) addUnlock(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unlockFns = append(m.unlockFns, fn)
}

func (m *MockTx) releaseAll() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	fns := m.unlockFns
	m.unlockFns = nil
	m.mu.Unlock()

	for _, fn := range fns {
		fn()
	}
}

func (m *MockTx) Commit(ctx context.Context) error {
	m.releaseAll()
	return nil
}

func (m *MockTx) Rollback(ctx context.Context) error {
	m.releaseAll()
	return nil
}

type MockTxManager struct{}

func (m *MockTxManager) Begin(ctx context.Context) (pgx.Tx, error) {
	return &MockTx{}, nil
}

// InMemoryWalletRepo
type InMemoryWalletRepo struct {
	mu           sync.RWMutex
	walletLocks  map[uuid.UUID]*sync.Mutex
	wallets      map[uuid.UUID]*wallet.Wallet
	playerWallet map[string]uuid.UUID // playerID:currency -> walletID
}

func NewInMemoryWalletRepo() *InMemoryWalletRepo {
	return &InMemoryWalletRepo{
		walletLocks:  make(map[uuid.UUID]*sync.Mutex),
		wallets:      make(map[uuid.UUID]*wallet.Wallet),
		playerWallet: make(map[string]uuid.UUID),
	}
}

func (r *InMemoryWalletRepo) getLock(id uuid.UUID) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.walletLocks[id]
	if !ok {
		l = &sync.Mutex{}
		r.walletLocks[id] = l
	}
	return l
}

func cloneWallet(w *wallet.Wallet) *wallet.Wallet {
	cloned, _ := wallet.Rehydrate(
		w.ID(),
		w.PlayerID(),
		w.Currency(),
		w.Balance(),
		w.Version(),
		w.CreatedAt(),
		w.UpdatedAt(),
	)
	return cloned
}

func (r *InMemoryWalletRepo) Create(ctx context.Context, tx pgx.Tx, w *wallet.Wallet) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%s:%s", w.PlayerID(), w.Currency())
	if _, exists := r.playerWallet[key]; exists {
		return repository.ErrConflict
	}
	r.wallets[w.ID()] = cloneWallet(w)
	r.playerWallet[key] = w.ID()
	return nil
}

func (r *InMemoryWalletRepo) GetByID(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.wallets[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return cloneWallet(w), nil
}

func (r *InMemoryWalletRepo) GetByIDForUpdate(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*wallet.Wallet, error) {
	l := r.getLock(id)
	l.Lock()

	if mtx, ok := tx.(*MockTx); ok {
		mtx.addUnlock(func() {
			l.Unlock()
		})
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.wallets[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return cloneWallet(w), nil
}

func (r *InMemoryWalletRepo) GetByPlayerAndCurrency(ctx context.Context, playerID uuid.UUID, currency string) (*wallet.Wallet, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	key := fmt.Sprintf("%s:%s", playerID, currency)
	wid, ok := r.playerWallet[key]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return cloneWallet(r.wallets[wid]), nil
}

func (r *InMemoryWalletRepo) UpdateBalance(ctx context.Context, tx pgx.Tx, w *wallet.Wallet) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.wallets[w.ID()]
	if !ok {
		return repository.ErrNotFound
	}
	if existing.Version() != w.Version()-1 {
		return fmt.Errorf("optimistic version mismatch")
	}
	r.wallets[w.ID()] = cloneWallet(w)
	return nil
}

// InMemoryTransactionRepo
type InMemoryTransactionRepo struct {
	mu           sync.RWMutex
	transactions map[uuid.UUID]*transaction.WagerTransaction
	txByExtID    map[string]*transaction.WagerTransaction
	txByIdempKey map[string]*transaction.WagerTransaction
	reversals    map[uuid.UUID]*transaction.WagerTransaction // parentID -> processed reversal
}

func NewInMemoryTransactionRepo() *InMemoryTransactionRepo {
	return &InMemoryTransactionRepo{
		transactions: make(map[uuid.UUID]*transaction.WagerTransaction),
		txByExtID:    make(map[string]*transaction.WagerTransaction),
		txByIdempKey: make(map[string]*transaction.WagerTransaction),
		reversals:    make(map[uuid.UUID]*transaction.WagerTransaction),
	}
}

func (r *InMemoryTransactionRepo) Create(ctx context.Context, tx pgx.Tx, t *transaction.WagerTransaction) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t.ExternalTransactionID() != "" {
		extKey := fmt.Sprintf("%s:%s", t.ProviderID(), t.ExternalTransactionID())
		if _, exists := r.txByExtID[extKey]; exists {
			return repository.ErrConflict
		}
	}
	if t.IdempotencyKey() != "" {
		idempKey := fmt.Sprintf("%s:%s", t.ProviderID(), t.IdempotencyKey())
		if _, exists := r.txByIdempKey[idempKey]; exists {
			return repository.ErrConflict
		}
	}
	if (t.Kind() == transaction.KindRefund || t.Kind() == transaction.KindRollback) &&
		t.State() == transaction.StateProcessed && t.ResolvedInternalReferenceID() != nil {
		refID := *t.ResolvedInternalReferenceID()
		if _, exists := r.reversals[refID]; exists {
			return repository.ErrConflict
		}
		r.reversals[refID] = t
	}

	r.transactions[t.ID()] = t
	if t.ExternalTransactionID() != "" {
		r.txByExtID[fmt.Sprintf("%s:%s", t.ProviderID(), t.ExternalTransactionID())] = t
	}
	if t.IdempotencyKey() != "" {
		r.txByIdempKey[fmt.Sprintf("%s:%s", t.ProviderID(), t.IdempotencyKey())] = t
	}
	return nil
}

func (r *InMemoryTransactionRepo) Update(ctx context.Context, tx pgx.Tx, t *transaction.WagerTransaction) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if (t.Kind() == transaction.KindRefund || t.Kind() == transaction.KindRollback) &&
		t.State() == transaction.StateProcessed && t.ResolvedInternalReferenceID() != nil {
		refID := *t.ResolvedInternalReferenceID()
		if existing, exists := r.reversals[refID]; exists && existing.ID() != t.ID() {
			return repository.ErrConflict
		}
		r.reversals[refID] = t
	}
	r.transactions[t.ID()] = t
	return nil
}

func (r *InMemoryTransactionRepo) GetByID(ctx context.Context, id uuid.UUID) (*transaction.WagerTransaction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.transactions[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return t, nil
}

func (r *InMemoryTransactionRepo) GetByProviderAndExternalID(ctx context.Context, providerID, externalID string) (*transaction.WagerTransaction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.txByExtID[fmt.Sprintf("%s:%s", providerID, externalID)]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return t, nil
}

func (r *InMemoryTransactionRepo) GetByProviderAndIdempotencyKey(ctx context.Context, providerID, key string) (*transaction.WagerTransaction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.txByIdempKey[fmt.Sprintf("%s:%s", providerID, key)]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return t, nil
}

func (r *InMemoryTransactionRepo) HasProcessedReversal(ctx context.Context, tx pgx.Tx, parentID uuid.UUID) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.reversals[parentID]
	return exists, nil
}

func (r *InMemoryTransactionRepo) ListPendingReferences(ctx context.Context, limit int) ([]*transaction.WagerTransaction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var list []*transaction.WagerTransaction
	for _, t := range r.transactions {
		if t.State() == transaction.StatePendingReference {
			list = append(list, t)
			if len(list) >= limit {
				break
			}
		}
	}
	return list, nil
}

// InMemoryLedgerRepo
type InMemoryLedgerRepo struct {
	mu     sync.RWMutex
	ledger []*ledger.WalletLedgerEntry
}

func NewInMemoryLedgerRepo() *InMemoryLedgerRepo {
	return &InMemoryLedgerRepo{ledger: make([]*ledger.WalletLedgerEntry, 0)}
}

func (r *InMemoryLedgerRepo) Create(ctx context.Context, tx pgx.Tx, entry *ledger.WalletLedgerEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.ledger {
		if existing.WalletID() == entry.WalletID() && existing.TransactionID() == entry.TransactionID() {
			return repository.ErrConflict
		}
	}
	r.ledger = append(r.ledger, entry)
	return nil
}

func (r *InMemoryLedgerRepo) ListByWallet(ctx context.Context, walletID uuid.UUID, cursor *time.Time, limit int) ([]*ledger.WalletLedgerEntry, *time.Time, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var res []*ledger.WalletLedgerEntry
	for _, e := range r.ledger {
		if e.WalletID() == walletID {
			res = append(res, e)
		}
	}
	return res, nil, nil
}

func (r *InMemoryLedgerRepo) CalculateBalance(ctx context.Context, walletID uuid.UUID) (money.Money, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var totalCents int64 = 0
	count := 0
	curr := "BRL"
	for _, e := range r.ledger {
		if e.WalletID() == walletID {
			curr = e.Amount().Currency()
			count++
			if e.Direction() == ledger.Credit {
				totalCents += e.Amount().Cents()
			} else {
				totalCents -= e.Amount().Cents()
			}
		}
	}
	m, err := money.FromCents(totalCents, curr)
	return m, count, err
}

// InMemoryOutboxRepo
type InMemoryOutboxRepo struct {
	mu        sync.Mutex
	outbox    []*events.EventEnvelope
	published map[uuid.UUID]bool
	locked    map[uuid.UUID]bool
}

func NewInMemoryOutboxRepo() *InMemoryOutboxRepo {
	return &InMemoryOutboxRepo{
		outbox:    make([]*events.EventEnvelope, 0),
		published: make(map[uuid.UUID]bool),
		locked:    make(map[uuid.UUID]bool),
	}
}

func (r *InMemoryOutboxRepo) Create(ctx context.Context, tx pgx.Tx, env *events.EventEnvelope) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outbox = append(r.outbox, env)
	return nil
}

func (r *InMemoryOutboxRepo) FetchPendingForUpdate(ctx context.Context, tx pgx.Tx, limit int) ([]*events.EventEnvelope, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var batch []*events.EventEnvelope
	var lockedIDs []uuid.UUID
	for _, env := range r.outbox {
		if r.published[env.EventID] || r.locked[env.EventID] {
			continue
		}
		r.locked[env.EventID] = true
		lockedIDs = append(lockedIDs, env.EventID)
		batch = append(batch, env)
		if len(batch) >= limit {
			break
		}
	}

	if mtx, ok := tx.(*MockTx); ok && len(lockedIDs) > 0 {
		mtx.addUnlock(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			for _, id := range lockedIDs {
				delete(r.locked, id)
			}
		})
	}

	return batch, nil
}

func (r *InMemoryOutboxRepo) MarkPublished(ctx context.Context, tx pgx.Tx, eventID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.published[eventID] = true
	return nil
}

func (r *InMemoryOutboxRepo) IncrementRetry(ctx context.Context, tx pgx.Tx, eventID uuid.UUID, nextRetry time.Time) error {
	return nil
}

// InMemoryInboxRepo
type InMemoryInboxRepo struct {
	mu    sync.RWMutex
	inbox map[string]string
}

func NewInMemoryInboxRepo() *InMemoryInboxRepo {
	return &InMemoryInboxRepo{inbox: make(map[string]string)}
}

func (r *InMemoryInboxRepo) RegisterOrCheck(ctx context.Context, tx pgx.Tx, consumerName, messageID, payloadHash string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%s:%s", consumerName, messageID)
	if existing, ok := r.inbox[key]; ok {
		if existing != payloadHash {
			return false, fmt.Errorf("different hash")
		}
		return false, nil
	}
	r.inbox[key] = payloadHash
	return true, nil
}
