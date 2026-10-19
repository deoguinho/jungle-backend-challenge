# Decisões de arquitetura

Este documento resume as decisões implementadas para o [desafio Backend em Go da Jungle](https://github.com/junglegaming/backend-challenge-go). Detalhes de regras e operação estão nas specs de [aplicação](specs/APP_SPEC.md), [banco](specs/DATABASE_SPEC.md) e [ambiente local](specs/ARCHITECTURE_SPEC.md).

## Aplicação

- Go 1.26.1, Go Modules, Uber Fx, `net/http` e `pgx` com SQL explícito.
- DDD + Clean Architecture: `internal/domain` não depende de adapters; casos de uso dependem de portas; Fx monta as dependências nos binários em `cmd/`.
- `Money` usa `shopspring/decimal.Decimal` e PostgreSQL `NUMERIC`, sem conversão para ponto flutuante.
- O saldo é alterado por delta decimal em uma instrução SQL atômica (`balance = balance + delta`) com condição contra saldo negativo. Saldo, ledger e outbox ficam na mesma transação PostgreSQL.
- Erros de domínio são valores classificáveis e propagados com wrapping; HTTP classifica as respostas por `errors.Is`/`errors.As`.
- API, consumidor SQS, worker de referências, publisher da outbox e migrations são binários/funções operacionais separados.

## Idempotência e persistência

PostgreSQL guarda a chave, hash canônico, transação e snapshot do resultado, e é a fonte autoritativa depois de reinícios. Redis acelera a localização da transação em replays frequentes e reduz leituras repetidas no PostgreSQL. Cache ausente/indisponível não muda a decisão: o caso de uso consulta o PostgreSQL. Reutilizar chave com conteúdo diferente resulta em conflito.

A persistência financeira usa constraints, índices únicos, triggers e migrations numeradas. Lançamentos são append-only. A atualização de saldo e o lançamento no ledger são confirmados juntos. Referências ausentes ficam pendentes com retry persistido e expiração. A outbox registra eventos na transação financeira; um worker publica no SQS com lease e retentativas.

## Identidade e ambiente

Keycloak fornece OAuth 2.0/OIDC. A API valida assinatura, emissor e expiração do token e autoriza clientes internos ou do provedor configurado. O isolamento das consultas do provedor é aplicado nos casos de uso. Carteiras são restritas ao cliente interno.

Docker Compose executa PostgreSQL, Redis, Keycloak e MiniStack. O serviço de bootstrap cria/reutiliza o cluster kind, conecta seus nós à rede Compose, configura credenciais e URLs, compila/carrega a imagem, aplica migrations e workloads. API e workers executam no kind. As filas SQS, incluindo DLQ e destino de eventos, são criadas pelo script de inicialização.

## Execução e qualidade

Consulte o [README](../README.md) para pré-requisitos, execução, configuração local e comandos. O shutdown usa `fx.Lifecycle` para encerrar servidor e workers e fechar recursos.

A cobertura unitária atual supera 90% nos pacotes de domínio e aplicação (90,5% em `application`). Os testes integrados com PostgreSQL cobrem movimentos concorrentes em processos independentes, reenvio idempotente, inbox/reentrega entre processos e agendamento de retry da outbox. A inbox e o resultado financeiro são confirmados na mesma Unit of Work; reconciliação lê saldo e ledger em uma única consulta. O smoke verifica Keycloak, autorização, replay, SQS, reconciliação, outbox e métricas. `go test -race ./...` foi executado com CGO habilitado.
