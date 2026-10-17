# Jungle Backend Challenge

Implementação em Go do [desafio Backend da Jungle](https://github.com/junglegaming/backend-challenge-go), organizada em DDD + Clean Architecture. A API e os workers executam no kind; PostgreSQL, Redis, Keycloak e MiniStack são iniciados pelo Docker Compose.

## Documentação

- [Decisões arquiteturais](docs/ARCHITECTURE.md)
- [Spec da aplicação e regras de negócio](docs/specs/APP_SPEC.md)
- [Spec do banco e migrations](docs/specs/DATABASE_SPEC.md)
- [Spec do ambiente local](docs/specs/ARCHITECTURE_SPEC.md)

## Pastas

- `cmd/api`, `cmd/worker`, `cmd/migrate`: pontos de entrada.
- `internal/domain`: modelos e regras encapsuladas de carteira e transação.
- `internal/application`: casos de uso e portas.
- `internal/adapters`: HTTP/OIDC, PostgreSQL, Redis e SQS.
- `internal/platform`: configuração operacional.
- `migrations`: migrations SQL versionadas.
- `deploy`: kind, manifests Kubernetes, Keycloak e inicialização de filas.
- `docs/specs`: decisões detalhadas que orientam a solução.

## Pré-requisitos

Docker Engine/Desktop com Compose v2, `kind`, `kubectl`, GNU Make e Go 1.26.1. No Windows, execute os comandos `make` em ambiente com GNU Make (por exemplo, WSL).

## Configuração e execução local

Copie `.env.example` para `.env` se quiser alterar os valores locais. Os valores padrão são fictícios e destinados apenas ao ambiente local.

```sh
docker compose up --build
```

O Compose inicia as dependências, cria as filas SQS, cria/reutiliza o cluster kind, compila e carrega a imagem, aplica migrations e sobe API e workers. A API fica em `http://localhost:8080`; Keycloak em `http://localhost:8081` e MiniStack em `http://localhost:4566`.

Comandos equivalentes via Make:

```sh
make up       # inicia dependências e provisiona o kind
make status   # lista serviços e pods
make logs     # acompanha os logs do Compose
make down     # para o Compose; preserva o cluster kind
make cluster-down
```

`make migrate-up` reaplica o Job de migrations. `make migrate-down` executa rollback de uma migration; migrations com dados existentes podem recusar rollback para preservar integridade. Para executar a API fora do cluster, primeiro inicie as dependências, exporte `DATABASE_URL`, `REDIS_ADDR`, `AWS_ENDPOINT_URL`, `SQS_QUEUE_URL`, `SQS_EVENTS_QUEUE_URL`, `OIDC_ISSUER` e credenciais locais, e rode `go run ./cmd/api`. O fluxo recomendado é o Compose completo, pois provisiona os endpoints acessíveis aos Pods.

O realm `jungle` é importado automaticamente. Clientes locais e credenciais de desenvolvimento estão definidos em `deploy/keycloak/realm-jungle.json`; use-os somente localmente. A inicialização de filas e DLQ está em `deploy/ministack/init-sqs.sh`.

Para obter um token de serviço interno e abrir uma carteira (requer `curl` e `jq`):

```sh
TOKEN=$(curl -s -u 'jungle-api:jungle-api-secret-local-only' \
  -d 'grant_type=client_credentials' \
  http://localhost:8081/realms/jungle/protocol/openid-connect/token | jq -r .access_token)
curl -i http://localhost:8080/wallets \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","initialBalance":{"amount":"100.00","currency":"BRL"}}'
```

Para enviar uma operação autenticada como provedor, obtenha um token com o cliente `jungle-provider-local` e seu segredo local (`jungle-provider-secret-local-only`), então envie `POST /wagering/transactions` com `Idempotency-Key` e o JSON definido em [APP_SPEC.md](docs/specs/APP_SPEC.md). O ID do provedor de desenvolvimento é `local`.

## Verificações

```sh
go test ./...
go test -race ./...
go vet ./...
make coverage
```

Os testes atuais cobrem os pacotes de domínio e aplicação. A meta de 90% de cobertura unitária e os cenários de integração/concorrência descritos nas specs ainda exigem expansão; consulte [APP_SPEC.md](docs/specs/APP_SPEC.md) antes de considerar a implementação pronta para avaliação do desafio.
