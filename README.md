# Jungle Backend Challenge

Implementação em Go do [desafio Backend da Jungle](https://github.com/junglegaming/backend-challenge-go), organizada em DDD + Clean Architecture. A API e os workers executam no kind; PostgreSQL, Redis, Keycloak e MiniStack são iniciados pelo Docker Compose.

## Documentação

- [Decisões arquiteturais](docs/ARCHITECTURE.md)
- [Decisões arquiteturais exigidas na raiz](ARCHITECTURE.md)
- [Spec da aplicação e regras de negócio](docs/specs/APP_SPEC.md)
- [Spec do banco e migrations](docs/specs/DATABASE_SPEC.md)
- [Spec do ambiente local](docs/specs/ARCHITECTURE_SPEC.md)
- [Contrato da API (OpenAPI)](docs/openapi.yaml)

A API exporta métricas Prometheus em `GET /metrics`. Cada worker expõe as métricas do próprio processo em `GET /metrics` na porta `9090`, incluindo retries SQS/referência/outbox, profundidade aproximada da DLQ e idade do evento pendente mais antigo. Métricas são por processo; em Kubernetes, use `api:8080/metrics`, `worker-sqs-metrics:9090/metrics`, `worker-reference-metrics:9090/metrics` e `worker-outbox-metrics:9090/metrics` para agregar as réplicas.

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

Para subir o ambiente completo: Docker Desktop/Engine com Compose v2. O `kind` e o `kubectl` usados pelo bootstrap estão no container de infraestrutura. GNU Make é necessário apenas para os comandos `make`; Go 1.26.1 é necessário para executar testes ou rodar os binários fora do Compose.

## Executar localmente

Pré-requisitos: Docker Desktop/Engine com Compose v2. No Windows, abra o terminal na pasta do repositório e confirme que o Docker Desktop está iniciado. Copie `.env.example` para `.env` somente se quiser personalizar os valores locais; os padrões já funcionam para desenvolvimento.

### 1. Iniciar dependências, Swagger e aplicação

```powershell
docker compose up -d --build
```

O Compose inicia PostgreSQL, Redis, Keycloak, MiniStack e Swagger UI. O serviço `kind-bootstrap` cria ou reutiliza o cluster kind, compila e carrega a imagem, aplica migrations e inicia a API e os workers. Na primeira execução, aguarde o bootstrap concluir.

### 2. Confirmar que está pronto

```powershell
docker compose ps
Invoke-RestMethod http://localhost:8088/health/ready
docker exec jungle-local-control-plane kubectl --kubeconfig=/etc/kubernetes/admin.conf -n jungle get pods
```

A readiness deve indicar `ready`; os pods `api`, `worker-sqs`, `worker-reference` e `worker-outbox` devem estar `Running`.

### 3. Abrir e usar o Swagger

Acesse **[http://localhost:8082](http://localhost:8082)**. A interface carrega o contrato [docs/openapi.yaml](docs/openapi.yaml) e permite executar as rotas da API, disponível em **http://localhost:8088**. Keycloak fica em `http://localhost:8081` e MiniStack em `http://localhost:4566`.

No PowerShell, gere um token para a rota que deseja testar. Use o cliente interno para carteiras e consultas internas:

```powershell
$response = curl.exe -sS -u "jungle-api:jungle-api-secret-local-only" `
  -d "grant_type=client_credentials" `
  http://localhost:8081/realms/jungle/protocol/openid-connect/token | ConvertFrom-Json
$token = $response.access_token
Set-Clipboard $token
```

Para operações de wagering, obtenha o token com `jungle-provider-local:jungle-provider-secret-local-only`. No Swagger, clique em **Authorize**, cole o token sem o prefixo `Bearer ` e autorize. Se trocar entre rotas internas e de provedor, gere e autorize o token do cliente correspondente.

### 4. Rodar a demonstração ponta a ponta (opcional)

```powershell
docker compose run --rm --entrypoint /bin/bash kind-bootstrap /workspace/scripts/smoke-local.sh
```

O smoke test autentica no Keycloak, cria uma carteira, envia uma aposta HTTP, confere o replay idempotente, consome uma mensagem SQS e reconcilia o saldo e os eventos da outbox.

Comandos equivalentes via Make:

```sh
make up       # inicia dependências/Swagger e provisiona o kind
make status   # lista serviços e pods
make logs     # acompanha os logs do Compose
make down     # para o Compose; preserva o cluster kind
make cluster-down
make smoke-local  # executa o fluxo ponta a ponta
```

`make migrate-up` reaplica o Job de migrations. `make migrate-down` executa rollback de uma migration; migrations com dados existentes podem recusar rollback para preservar integridade. Para executar a API fora do cluster, primeiro inicie as dependências, exporte `DATABASE_URL`, `REDIS_ADDR`, `AWS_ENDPOINT_URL`, `SQS_QUEUE_URL`, `SQS_EVENTS_QUEUE_URL`, `OIDC_ISSUER` e credenciais locais, e rode `go run ./cmd/api`. O fluxo recomendado é o Compose completo, pois provisiona os endpoints acessíveis aos Pods.

O realm `jungle` é importado automaticamente. Clientes locais e credenciais de desenvolvimento estão definidos em `deploy/keycloak/realm-jungle.json`; use-os somente localmente. As origens permitidas para o Swagger são configuradas em `CORS_ALLOWED_ORIGINS`; em outros ambientes, inclua apenas origens confiáveis. A inicialização de filas e DLQ está em `deploy/ministack/init-sqs.sh`.

## Verificações

```sh
go test ./...
go test -race ./...
go vet ./...
make coverage
```

No PowerShell deste workspace, o `-race` usa o MinGW portátil local. Configure o `GOENV` ignorado pelo Git antes de executar:

```powershell
$env:GOENV = "$PWD\.cache\go.env"
$env:GOCACHE = "$PWD\.cache\go-build"
go test -race ./...
```

Em outra máquina, instale um compilador C compatível e configure `CGO_ENABLED=1` e `CC` para o caminho local do compilador.

Para validar PostgreSQL real, concorrência, inbox e retries da outbox, configure uma base dedicada com as migrations aplicadas e execute:

```sh
docker compose exec -T postgres createdb -U jungle jungle_test
DATABASE_URL='postgres://jungle:jungle-local-only@localhost:5432/jungle_test?sslmode=disable' go run ./cmd/migrate up
TEST_DATABASE_URL='postgres://jungle:jungle-local-only@localhost:5432/jungle_test?sslmode=disable' make test-integration
make smoke-local
```

`make smoke-local` verifica readiness, rejeição sem token, autorização por cliente, isolamento entre provedores, abre carteira, processa aposta HTTP e replay, consome SQS, verifica a inbox, reconcilia saldo, confirma eventos da outbox, verifica o redrive de mensagem inválida à DLQ e consulta métricas. A API é publicada em `http://localhost:8088` (mapeada para o container na porta 8080).

Os pacotes de domínio e aplicação atendem à meta unitária de 90% (90,5% em `application`). No Windows, o comando `go test -race` precisa de CGO e de um compilador C; o arquivo `.cache/go.env` configura o compilador MinGW local deste workspace. Em outra máquina, configure `CGO_ENABLED=1` e `CC` para um compilador compatível.
