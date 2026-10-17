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

A cobertura atual é 92–98% nos pacotes de domínio, mas apenas 3,6% em `application`. A meta de 90%, integração real com PostgreSQL/MiniStack/Keycloak, concorrência entre múltiplas instâncias e cenários de falha do desafio ainda precisam ser completados antes de uma avaliação integral. Um limite conhecido é que o registro/conclusão da inbox SQS ainda ocorre fora da mesma transação PostgreSQL que executa o caso de uso; a idempotência financeira persistente evita reaplicação, mas não satisfaz a atomicidade inbox + domínio pedida no README de referência. A verificação de reconciliação também deve ser ajustada para ler saldo e ledger no mesmo snapshot consistente. `go test -race ./...` ainda requer CGO e um compilador C disponível no ambiente de desenvolvimento.
