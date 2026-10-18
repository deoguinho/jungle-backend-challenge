# Decisões de arquitetura

Resumo das decisões desta entrega para o [desafio Jungle](https://github.com/junglegaming/backend-challenge-go). As regras completas estão em [APP_SPEC](docs/specs/APP_SPEC.md), [DATABASE_SPEC](docs/specs/DATABASE_SPEC.md) e [ARCHITECTURE_SPEC](docs/specs/ARCHITECTURE_SPEC.md).

## Aplicação e domínio

- Go com DDD e Clean Architecture: domínio sem dependência de adapters, casos de uso dependem de portas e `cmd/` faz a composição.
- Dinheiro é representado por `decimal.Decimal` e persistido em PostgreSQL `NUMERIC`; valores nunca passam por ponto flutuante.
- Saldo é atualizado por delta em SQL (`balance = balance + delta`) com condição contra saldo negativo. Saldo, ledger, transação e outbox são gravados na mesma transação.
- Lançamentos são append-only; constraints, índices únicos e triggers protegem as invariantes também no banco.
- Erros são classificados com `errors.Is`/`errors.As`; a API converte categorias em respostas estáveis.

## Idempotência e mensageria

- Redis acelera leituras de replay e reduz leituras repetidas no PostgreSQL. É cache descartável; PostgreSQL continua sendo a fonte durável e autoritativa.
- Inbox deduplica mensagens na mesma transação do efeito financeiro. SQS só confirma a mensagem depois do commit; reentrega é segura.
- Outbox grava eventos junto com a operação financeira e um worker publica no SQS com lease e retentativas. `eventId` permanece estável.
- Referências ausentes ficam pendentes no PostgreSQL, com tentativas agendadas e expiração de 24 horas.

## Identidade e ambiente

- Keycloak emite tokens OIDC. A API valida assinatura, emissor e validade e restringe operações por tipo de cliente.
- Docker Compose executa PostgreSQL, Redis, Keycloak e MiniStack. O bootstrap cria o cluster kind, configura dependências, constrói/carrega a imagem, aplica migrations e inicia API e workers.
- A especificação de segurança financeira para produção, reconciliação operacional e garantias além do escopo atual deve ser refinada antes de uso financeiro real.

## Verificação e limitações

- `make test-race`, `make test-integration TEST_DATABASE_URL=...`, `make smoke-local` e `make coverage` são os comandos de validação documentados.
- A integração PostgreSQL verifica concorrência, idempotência, inbox e retry da outbox. `make smoke-local` verifica o fluxo HTTP autenticado e processamento SQS no ambiente Compose/kind.
- O cache Redis pode ser perdido sem perder a decisão de idempotência; isso aumenta leituras no PostgreSQL até o cache ser populado novamente.
- O ambiente local usa credenciais de desenvolvimento e não representa configuração de produção.
