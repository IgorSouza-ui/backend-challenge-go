# Jungle Gaming — Distributed Wagering & Wallet Financial Service (Go)

Serviço financeiro distribuído para processamento de transações de apostas (`BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK`), gestão de carteiras (`Wallet`) e razão contábil imutável (`WalletLedgerEntry`), construído em **Go** com **Uber Fx**, **PostgreSQL**, **AWS SQS FIFO (LocalStack)**, **Keycloak (OAuth2/OIDC)** e **Prometheus**.

Para detalhes profundos sobre as decisões de design, invariantes de domínio, concorrência e segurança, consulte o documento [**ARCHITECTURE.md**](./ARCHITECTURE.md).

---

## 1. Pré-requisitos

- **Go 1.24+** (para execução local de testes e build)
- **Docker** e **Docker Compose** (para subir PostgreSQL, LocalStack, Keycloak e a aplicação)
- `curl` e `jq` (opcional, para testar as requisições HTTP via terminal)

---

## 2. Configuração de Ambiente (`.env`)

Um arquivo de exemplo está disponível em [`.env.example`](./.env.example). Para criar seu `.env` local:

```bash
cp .env.example .env
```

Variáveis disponíveis:
| Variável | Padrão | Descrição |
| :--- | :--- | :--- |
| `PORT` | `8080` | Porta HTTP da aplicação |
| `POSTGRES_HOST` | `localhost` | Host do PostgreSQL (`postgres` no Docker Compose) |
| `POSTGRES_PORT` | `5432` | Porta do PostgreSQL |
| `POSTGRES_USER` | `postgres` | Usuário do banco |
| `POSTGRES_PASSWORD` | `postgres` | Senha do banco |
| `POSTGRES_DB` | `jungle_wager` | Nome do banco de dados |
| `POSTGRES_SSLMODE` | `disable` | Modo SSL do PostgreSQL |
| `SQS_ENDPOINT` | `http://localhost:4566` | Endpoint do LocalStack / AWS SQS |
| `SQS_REGION` | `us-east-1` | Região AWS |
| `SQS_QUEUE_URL` | `http://localhost:4566/000000000000/wager-transactions.fifo` | Fila FIFO de entrada de transações |
| `SQS_EVENTS_QUEUE_URL` | `http://localhost:4566/000000000000/wager-events.fifo` | Fila FIFO de saída de eventos de domínio (Outbox) |
| `JWT_SECRET` | `super-secret-keycloak-mock-secret-32-bytes!` | Segredo HMAC para testes locais |
| `JWT_ISSUER` | `http://localhost:8081/realms/jungle` | URL do realm OIDC no Keycloak |

---

## 3. Como Subir o Ambiente Completo (`docker compose`)

Suba toda a infraestrutura (PostgreSQL, LocalStack com provisionamento automático das filas FIFO e DLQ, Keycloak com importação automática do realm `jungle`, e o serviço Go):

```bash
docker compose up --build -d
```

Verifique a saúde do serviço:

```bash
curl -s http://localhost:8080/health/live
curl -s http://localhost:8080/health/ready
```

### Migrations (Aplicação e Reversão)
As migrations localizadas em [`migrations/000001_init_schema.up.sql`](./migrations/000001_init_schema.up.sql) são executadas automaticamente:
1. Pelo container do PostgreSQL na inicialização (`/docker-entrypoint-initdb.d/`).
2. Pela própria aplicação Go na subida do pool de conexões (de forma idempotente via `CREATE TABLE IF NOT EXISTS`).

**Para aplicar manualmente as migrations:**
```bash
psql "postgres://postgres:postgres@localhost:5432/jungle_wager?sslmode=disable" -f migrations/000001_init_schema.up.sql
```

**Para reverter as migrations (Rollback):**
```bash
psql "postgres://postgres:postgres@localhost:5432/jungle_wager?sslmode=disable" -f migrations/000001_init_schema.down.sql
```

---

## 4. Como Obter Tokens OAuth2 no Keycloak

O Keycloak sobe na porta `8081` com o realm `jungle` pré-configurado ([`keycloak/jungle-realm.json`](./keycloak/jungle-realm.json)) contendo três clientes confidenciais (`client_credentials`):

1. **`internal-service`** (secret: `internal-secret`) — possui a role `internal` para criar/consultar carteiras, ledger e reconciliação.
2. **`provider-a`** (secret: `provider-a-secret`) — possui `providerId = "provider-a"` e role `provider`.
3. **`provider-b`** (secret: `provider-b-secret`) — possui `providerId = "provider-b"` e role `provider`.

### Obtendo token do Serviço Interno (`INTERNAL_TOKEN`):
```bash
INTERNAL_TOKEN=$(curl -s -X POST "http://localhost:8081/realms/jungle/protocol/openid-connect/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "grant_type=client_credentials" \
  -d "client_id=internal-service" \
  -d "client_secret=internal-secret" | jq -r '.access_token')
```

### Obtendo token do Provedor A (`PROVIDER_A_TOKEN`):
```bash
PROVIDER_A_TOKEN=$(curl -s -X POST "http://localhost:8081/realms/jungle/protocol/openid-connect/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "grant_type=client_credentials" \
  -d "client_id=provider-a" \
  -d "client_secret=provider-a-secret" | jq -r '.access_token')
```

---

## 5. Exemplos de Uso da API HTTP

### 5.1. Criar Carteira (`POST /wallets` — requer `INTERNAL_TOKEN`)
```bash
curl -i -X POST "http://localhost:8080/wallets" \
  -H "Authorization: Bearer $INTERNAL_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "playerId": "11111111-1111-1111-1111-111111111111",
    "initialBalance": {
      "amount": "100.00",
      "currency": "BRL"
    }
  }'
```

### 5.2. Processar Aposta (`POST /wagering/transactions` — requer `PROVIDER_A_TOKEN`)
Substitua `<WALLET_ID>` pelo `id` retornado na criação da carteira:
```bash
curl -i -X POST "http://localhost:8080/wagering/transactions" \
  -H "Authorization: Bearer $PROVIDER_A_TOKEN" \
  -H "Idempotency-Key: provider-a:bet-001" \
  -H "Content-Type: application/json" \
  -d '{
    "providerId": "provider-a",
    "externalTransactionId": "bet-001",
    "playerId": "11111111-1111-1111-1111-111111111111",
    "walletId": "<WALLET_ID>",
    "roundId": "round-10",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": {
      "amount": "25.00",
      "currency": "BRL"
    }
  }'
```

### 5.3. Consultar Carteira e Ledger (`GET /wallets/{walletId}` e `/ledger`)
```bash
curl -s -H "Authorization: Bearer $INTERNAL_TOKEN" "http://localhost:8080/wallets/<WALLET_ID>" | jq
curl -s -H "Authorization: Bearer $INTERNAL_TOKEN" "http://localhost:8080/wallets/<WALLET_ID>/ledger?limit=50" | jq
```

### 5.4. Executar Reconciliação Contábil (`POST /wallets/{walletId}/reconciliation`)
```bash
curl -s -X POST -H "Authorization: Bearer $INTERNAL_TOKEN" \
  "http://localhost:8080/wallets/<WALLET_ID>/reconciliation" | jq
```

### 5.5. Métricas Prometheus (`GET /metrics`)
```bash
curl -s http://localhost:8080/metrics | grep wager_
```

---

## 6. Como Executar os Testes e Verificações de Qualidade

A suíte de testes cobre domínio (`Money`, `Wallet`, `WalletLedgerEntry`, `WagerTransaction`, `Idempotency`), concorrência na mesma carteira (incluindo o caso obrigatório de duas apostas simultâneas de `80.00 BRL` em saldo de `100.00 BRL`), múltiplas instâncias, idempotência sob 50 goroutines simultâneas, resolução e expiração de `PENDING_REFERENCE`, combinações de `REFUND`/`ROLLBACK`, deduplicação HTTP + SQS, múltiplos publicadores de Outbox, isolamento OAuth2/JWT entre provedores e ciclo de vida do Uber Fx.

```bash
# Executar todos os testes
go test -v ./...

# Executar testes com detector de condição de corrida (requer CGO_ENABLED=1 e GCC)
go test -race ./...

# Executar análise estática do Go
go vet ./...

# Verificar formatação do código
gofmt -s -w .
```
