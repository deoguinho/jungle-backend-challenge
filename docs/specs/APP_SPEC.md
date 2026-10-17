# Spec da aplicação

## Objetivo e escopo

Este documento descreve a organização da aplicação para o desafio [backend-challenge-go](https://github.com/junglegaming/backend-challenge-go), usando DDD e Clean Architecture. Registra domínios identificáveis, limites de camadas, regras de negócio, casos de uso, erros e a estrutura de diretórios implementada.

A árvore abaixo corresponde ao código existente. Esta spec continua como referência das regras funcionais e deve ser atualizada quando a implementação evoluir. As decisões devem permanecer compatíveis com o README do desafio e com [`ARCHITECTURE.md`](../../ARCHITECTURE.md), [`ARCHITECTURE_SPEC.md`](ARCHITECTURE_SPEC.md) e [`DATABASE_SPEC.md`](DATABASE_SPEC.md).

## Princípios arquiteturais

- **Domínio no centro:** entidades, value objects, regras e eventos de domínio não importam HTTP, SQS, Redis, PostgreSQL, Uber Fx ou bibliotecas externas.
- **Dependências apontam para dentro:** adapters de entrada chamam casos de uso; casos de uso dependem de portas/interfaces; adapters de saída implementam essas portas.
- **Agregados protegem invariantes:** estado mutável é privado. Não expor setters ou estruturas que permitam alterar saldo, estado de transação ou lançamentos sem passar por métodos validados.
- **Criação não é reidratação:** construtores de criação validam dados e aplicam regras iniciais. Repositórios reidratam estado persistido sem reaplicar movimentações, transições ou emissão de eventos.
- **Erros são valores tipados:** regras esperadas retornam erros classificáveis. `panic` não representa falha de negócio. Propagar contexto e preservar causa com wrapping.
- **Fx compõe, não define o domínio:** Fx só aparece na camada de composição/infraestrutura. Ciclo de vida é gerido com `fx.Lifecycle`, com shutdown gracioso de API e workers.
- **HTTP e SQS compartilham aplicação:** ambos adaptam mensagens para os mesmos casos de uso e regras de idempotência.
- **Sem lógica financeira em adapters:** handlers, consumers e repositórios não decidem se uma operação é válida; essa decisão pertence aos casos de uso e ao domínio, com persistência protegendo concorrência e invariantes.

## Estrutura de diretórios implementada

```text
cmd/
  api/                         # composição e processo HTTP
  worker/                      # consumidor SQS, referências e outbox
  migrate/                     # execução explícita de migrations
internal/
  domain/
    shared/                    # Money e erros de domínio
    wallet/                    # carteira e lançamentos
    wagering/                  # transações, abertura e reversões
  application/                 # casos de uso, portas e contratos de eventos
  adapters/
    inbound/httpapi/           # rotas, OIDC, DTOs e respostas HTTP
    outbound/postgres/         # Unit of Work e repositórios pgx
    outbound/redis/            # cache rápido de idempotência
    outbound/aws/               # cliente SQS
  platform/config/             # configuração e validação de ambiente
migrations/                    # migrations SQL up/down versionadas
deploy/                        # Compose, kind, Kubernetes, Keycloak e SQS
    lifecycle/                 # shutdown e coordenação Fx
```

`domain/` e `application/` são internos ao serviço, mas não devem depender de `adapters/` ou `platform/`. Pacotes podem ser reorganizados quando a implementação começar, desde que os limites e dependências acima permaneçam.

## Domínios e responsabilidades

### Shared Kernel

Tipos compartilhados sem dependência de infraestrutura:

  - **Money:** value object imutável baseado em `decimal.Decimal` (biblioteca `github.com/shopspring/decimal`) e moeda ISO 4217; valida escala de duas casas na entrada externa, operações exigem moeda compatível e preservam precisão exata.
- **Identificadores:** tipos distintos para WalletID, PlayerID, TransactionID, ProviderID, RoundID, GameID e EventID para evitar trocar strings/UUIDs por engano.
- **Clock/Time:** instantes UTC e abstração de relógio quando a regra exige cálculo de TTL/retry testável.
- **DomainError:** categorias e tipos de erro de domínio, sem códigos HTTP ou tipos de SDK.

### Wallet

Agregado raiz financeiro. `Wallet` encapsula ID, jogador, moeda, saldo, versão e timestamps. Só seus métodos de domínio alteram estado.

Responsabilidades: criação/reidratação, validação de moeda, crédito/débito, saldo não negativo, incremento de versão somente em mudança efetiva de saldo e construção de ledger correspondente. A persistência garante atomicidade/concurrency conforme [`DATABASE_SPEC.md`](DATABASE_SPEC.md).

### Wagering

Agregado `WagerTransaction` e regras das operações externas/internas. Tipos: `OPENING`, `BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK`. Estados: `PENDING`, `PENDING_REFERENCE`, `PROCESSED`, `REJECTED`, `FAILED`.

Responsabilidades: validar transições de estado, operação e referências, replay do resultado persistido e geração de eventos tipados. `OPENING` é interno, exclusivo da abertura da carteira.

### Idempotency

Representa identidade e conteúdo de uma solicitação idempotente: provider, chave, hash canônico e resultado original. Redis é cache de leitura rápida; o registro durável é necessário para replay após reinícios e a decisão de coordenação entre cache e PostgreSQL segue a spec de arquitetura/banco.

### Messaging / Integration

Inbox e outbox são mecanismos de aplicação/infraestrutura, não agregados de negócio. A aplicação solicita registro durável da mensagem e evento; adapters tratam protocolo SQS, visibility timeout, envio, polling e confirmação.

### Identity / Authorization

O adapter OIDC valida assinatura, issuer, audiência, expiração e claims. A aplicação recebe um `AuthenticatedPrincipal` tipado com permissões e provider autorizado. Não aceitar `providerId` do payload como autoridade. Credenciais não fazem parte do domínio de carteira/apostas.

## Entidades e value objects

### Money

- Criar por parsing decimal exato a partir de string (`"25.00"`) e moeda; nunca converter o valor por `float32`/`float64`. Rejeitar vazio, sinal negativo em entrada financeira, `NaN`, `Infinity`, notação científica e casas excedentes.
- Não arredondar entrada inválida. Se formatos equivalentes forem aceitos, normalizar antes do hash de idempotência.
- Permitir zero onde a regra do tipo permite. Valores negativos só em cálculos/diferenças internas, nunca como saldo.
- Soma, subtração e comparação requerem a mesma moeda. Validar limites de magnitude e escala compatíveis com o tipo SQL usado na persistência.

### Wallet

- Identidade única por `(playerId, currency)`.
- Saldo inicial pode ser zero ou positivo; versão inicial é `1`.
- `Debit` só aceita valor positivo na movimentação, mesma moeda e saldo suficiente; saldo final não pode ser negativo.
- `Credit` só aceita valor positivo e mesma moeda.
- Incrementar versão apenas quando saldo muda. `LOSS` não altera saldo nem versão.
- Alteração efetiva sempre produz exatamente um `WalletLedgerEntry` coerente com saldo anterior, direção, valor e saldo posterior.
- Saldo, identidade, moeda e versão não têm setters públicos. Reidratação preserva o estado armazenado sem gerar eventos nem ledger.

### WagerTransaction

- Toda operação externa contém provider, ID externo, chave de idempotência, hash canônico, player, wallet, round, game, tipo e Money; referência externa é opcional conforme tipo.
- `OPENING` contém identidade interna estável, wallet/player/currency, valor, estado e timestamps; campos externos não se aplicam.
- Estados terminais (`PROCESSED`, `REJECTED`, `FAILED`) não transitam novamente. Replays consultam o resultado persistido e não reaplicam o efeito.
- Operações sem dependência podem terminar sincronamente; operação confirmada como pendente precisa ter retomada durável.
- Falhas transitórias de infraestrutura não devem ser gravadas como `FAILED` terminal sem política explícita; falha permanente auditável pode ser terminal.

### WalletLedgerEntry

- Imutável; registra wallet, transaction, direção, valor positivo, saldo anterior/posterior e instante UTC.
- Crédito: `balanceAfter = balanceBefore + amount`; débito: `balanceAfter = balanceBefore - amount`, com saldo anterior suficiente.
- Unicidade por `(walletId, transactionId)`. Não há edição ou exclusão para corrigir erro; correção cria nova operação/lançamento permitido pelo domínio.

## Regras de negócio

### Abertura da carteira (`OPENING`)

- `POST /wallets` exige player e saldo inicial válido.
- Uma única carteira existe para cada par jogador/moeda; duplicidade retorna conflito.
- Saldo inicial zero cria a carteira com versão 1, sem `OPENING`, ledger ou evento financeiro.
- Saldo inicial positivo cria carteira e `OPENING` `PROCESSED`, um crédito no ledger e outbox dos eventos `WagerTransactionProcessed` e `WalletBalanceChanged`, com versão 1, no mesmo commit.
- `OPENING` recebido externamente por HTTP/SQS é entrada inválida e nunca movimenta fundos.

### Operações externas

Regras comuns:

- Operação pertence ao provider autenticado; `providerId` de payload deve coincidir com o provider autorizado ou ser substituído pela identidade derivada, nunca usado para elevar autorização.
- O player, wallet, moeda e demais relações devem corresponder. Moeda da operação deve ser igual à da carteira.
- Header `Idempotency-Key` é obrigatório em HTTP; SQS usa `data.idempotencyKey`.
- Hash determinístico cobre campos de negócio normalizados, excluindo chave e metadados de transporte. A mesma chave/conteúdo retorna resultado persistido; mesma chave com hash diferente retorna conflito.
- `(providerId, externalTransactionId)` também é único, mesmo com chave diferente.
- A resposta de replay de operação concluída mantém o saldo observado no processamento original, não o saldo atual.
- Valores monetários externos são decimais string, duas casas; entradas externas negativas são rejeitadas.

Regras por tipo:

| Tipo | Efeito | Condições |
|---|---|---|
| `BET` | Débito | Valor > 0 e saldo suficiente; cria ledger de débito. |
| `WIN` | Crédito | Valor > 0; pode referenciar aposta da mesma rodada. |
| `LOSS` | Sem movimento | Valor deve ser exatamente `0.00`; sem ledger/alteração de versão, mas finaliza `PROCESSED` e produz `WagerTransactionProcessed`. |
| `REFUND` | Crédito | Valor > 0; referência obrigatória a `BET` processada; devolução integral do valor original. |
| `ROLLBACK` | Movimento contrário | Valor > 0; referência obrigatória a `BET`, `WIN` ou `REFUND` processada; valor integral e efeito oposto ao referenciado. |

Referências/reversões:

- Resolver referência por `(providerId, referenceExternalTransactionId)` e conferir provider, jogador, wallet, moeda e round iguais.
- Referência ausente cria `PENDING_REFERENCE`; retry durável aplica backoff exponencial. TTL ou máximo de tentativas, ainda a parametrizar, termina em `REJECTED` com código estável de referência não encontrada e evento de rejeição.
- Referência existente em estado pendente não pode ser aplicada ainda; aguardar/reagendar. Referência `REJECTED`/`FAILED` não pode ser revertida; terminar com rejeição definitiva explicável.
- Reversão parcial não é suportada.
- **Política contra devolução duplicada:** uma operação de origem não pode ter duas reversões bem-sucedidas que devolvam o mesmo efeito. `REFUND` e `ROLLBACK` diretamente sobre a mesma `BET` são mutuamente exclusivos. Um `ROLLBACK` de `REFUND` deve referenciar a transação `REFUND` (não a `BET`) e desfaz o crédito dessa devolução. A regra deve ser validada no domínio e protegida por constraint/índice único no banco.
- Se um `ROLLBACK` que debita uma carteira não tiver saldo disponível, rejeitar e auditar com `failureCode` próprio, distinto de saldo insuficiente em `BET`.
- Toda rejeição tem `failureCode` estável, separando entrada corrigível de resultado definitivo.

### Consulta e reconciliação

- `GET /wallets/:walletId`: carteira visível somente a caller autorizado conforme política de autorização definida.
- `GET /wallets/:walletId/ledger`: paginação por cursor opaco e ordenação estável.
- Consulta interna por transaction ID e consulta de provider por `(providerId, externalTransactionId)` respeitam isolamento de provider inclusive em replay.
- Reconciliação soma créditos e débitos do ledger, incluindo crédito inicial, compara com saldo armazenado em visão consistente, retorna `storedBalance`, `calculatedBalance`, `difference`, `consistent` e quantidade de entradas. Não altera dados; divergência também vai a logs e métrica.

## Casos de uso

| Caso de uso | Entrada principal | Resultado e regras |
|---|---|---|
| `OpenWallet` | player, initial Money | Cria uma carteira única; abertura positiva inclui transação/ledger/eventos; zero não cria movimento. |
| `GetWallet` | principal, wallet ID | Retorna carteira somente após autorização. |
| `ListWalletLedger` | principal, wallet ID, cursor/limit | Retorna página estável e autorizada do ledger. |
| `SubmitWagerTransaction` | principal/provider, chave, operação | Valida autorização, normaliza/hash, coordena idempotência, processa regra e retorna resultado/replay/conflito/pendência. Usado por HTTP. |
| `ConsumeWagerTransaction` | mensagem SQS | Valida envelope e hash, deduplica inbox e invoca o mesmo processamento de operação; confirma mensagem somente após resultado durável. |
| `GetWagerTransaction` | principal, transaction ID | Consulta estado e resultado sem reaplicar operação. |
| `GetProviderWagerTransaction` | principal, external ID | Impõe isolamento do provider autenticado. |
| `RetryPendingReference` | transação pendente | Reavalia referência, aplica operação uma vez ou agenda retry/rejeita ao atingir TTL/tentativas. |
| `PublishOutboxBatch` | limite de lote/worker | Adquire eventos pendentes, publica snapshot com `eventId` estável e registra tentativa/resultado; retries podem duplicar entrega, não o efeito de negócio. |
| `ReconcileWallet` | principal, wallet ID | Compara saldo e ledger sem modificar o saldo; reporta divergência. |
| `CheckReadiness` | dependências | Readiness verifica PostgreSQL e SQS; liveness verifica somente processo. |

`GetWallet`, listagem, leitura e reconciliação têm de aplicar as regras de autorização da API. Consumers internos usam identidade técnica com privilégios mínimos.

## Erros estruturados

Em Go, a convenção correta é `errors.Is()` e `errors.As()` (pacote padrão `errors`), não `Error.Is()`. Cada erro deve preservar sua causa com `%w` ou mecanismo equivalente para que callers possam classificá-lo sem comparar texto.

### Categorias

- **Validação/contrato:** JSON inválido, campo ausente, Money inválido, tipo externo proibido (`OPENING`).
- **Autorização:** token ausente/inválido/expirado, provider não autorizado, operação interna acessada externamente.
- **Conflito/idempotência:** chave repetida com hash diferente, external transaction ID reutilizado, carteira já existente.
- **Rejeição de domínio:** saldo insuficiente em `BET`, saldo insuficiente em `ROLLBACK`, moeda incompatível, referência inexistente/inelegível, reversão duplicada.
- **Não encontrado:** carteira/transação ausente, respeitando política de não revelar recurso de outro provider.
- **Pendente:** referência aguardando chegada ou processamento assíncrono.
- **Dependência transitória:** PostgreSQL/Redis/SQS indisponível, timeout, deadlock/retry esgotado.
- **Falha interna/permanente:** corrupção/violação inesperada, estado persistido inválido, configuração inválida ou falha permanente auditável.

### Tipagem e wrapping

- Tipos/sentinelas de domínio expressam condições estáveis, por exemplo `ErrInsufficientFunds`, `ErrIdempotencyConflict`, `ErrReferencePending` e `ErrUnauthorized`; nomes finais podem evoluir.
- `errors.Is(err, Err...)` identifica uma condição conhecida mesmo após wrapping.
- `errors.As(err, &typedErr)` extrai detalhes tipados, como `FailureCode`, campo inválido, tipo de operação ou IDs de correlação não sensíveis.
- Erro de domínio pode conter `Code`, `Message` seguro e `Cause`, sem transportar status HTTP, nomes de filas ou detalhes SQL.
- Adapters traduzem erros de aplicação para HTTP (status/body) ou política SQS (ack/retry/DLQ). Texto de erro não é contrato; códigos estáveis são.
- Nunca incluir tokens, credenciais, payload financeiro completo ou dados sensíveis em mensagens/logs.
- Erros inesperados de infraestrutura são embrulhados com contexto (`operation`, dependency, correlation ID) e causa original preservada; não converter toda falha em erro de negócio.

### Tradução externa

O adapter HTTP mapeia erros categorizados para respostas distintas: entrada inválida, não autorizado/proibido, não encontrado, conflito de idempotência, rejeição de negócio, processamento pendente e indisponibilidade transitória. Os códigos/status e formatos exatos de resposta serão definidos na spec de API. O consumer SQS trata rejeição de negócio durável como terminal/ack, falha transitória com retry e mensagem inválida/permanente segundo política documentada de DLQ.

## Concorrência e atualização de saldo

Não fazer read-modify-write do saldo na memória da aplicação. O adapter PostgreSQL deve aplicar o delta diretamente sobre o valor atual do banco em uma única instrução SQL, usando parâmetro decimal exato/string convertido para `NUMERIC`, e condição que preserve saldo não negativo. Exemplo conceitual: `UPDATE ... SET balance = balance + $delta::numeric, version = version + 1 WHERE id = $wallet_id AND balance + $delta::numeric >= 0 RETURNING ...`. Crédito e débito usam o sinal apropriado depois da validação de domínio.

A atualização condicionada, o registro da operação e o ledger devem integrar a mesma transação SQL definida pela implementação. Conflito/nenhuma linha atualizada deve ser tratado como condição concorrente ou saldo insuficiente conforme o resultado da operação; retry é limitado e nunca pode descartar atualização confirmada. Carteiras diferentes não podem depender de lock global.

## Limites transacionais para os casos de uso

O caso de uso coordena portas de repositório e uma unidade de trabalho; o adapter PostgreSQL implementa o limite real. O limite de uma operação financeira deve considerar, conforme aplicável, transação de wagering, saldo da carteira, ledger, idempotência durável, inbox e eventos outbox. Nenhum evento externo é publicado antes do commit. A escolha exata de locks e a coordenação operacional Redis/PostgreSQL permanecem vinculadas a [`DATABASE_SPEC.md`](DATABASE_SPEC.md) e à decisão de implementação.

## Eventos de domínio/integração

Tipos publicados: `WagerTransactionProcessed` (inclusive `LOSS`), `WagerTransactionRejected`, `WalletBalanceChanged` (somente mudança real de saldo) e `WagerTransactionPendingReference`.

Eventos tipados são criados pelo domínio/aplicação depois de transição válida e persistidos como snapshot na outbox. Envelope contém `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId` opcional, `occurredAt` UTC RFC 3339, `version` e `data` tipado. `WalletBalanceChanged` carrega wallet, transaction, direção, Money, saldos anterior/posterior e versão da carteira. Publicação repetida conserva `eventId`.

## Ciclo de vida e dependências

- Uber Fx organiza módulos e construção de adapters/casos de uso via `fx.Module`, `fx.Provide` e `fx.Invoke`; nenhum pacote de domínio importa Fx.
- Startup valida configuração e dependências requeridas antes de readiness.
- `SIGTERM` interrompe entrada HTTP e polling SQS, deixa trabalho corrente terminar dentro do prazo ou permite reentrega segura, encerra publishers/workers e então fecha Redis/PostgreSQL/SQS clients.
- Cada operação de I/O recebe `context.Context`, timeout e cancelamento.
- Liveness distingue processo vivo de dependência pronta. Falha transitória de dependência não causa loop de reinício por liveness.

## Itens a fechar antes da implementação

- Status e corpo exatos de resposta HTTP para cada categoria de erro.
- Limites numéricos/maior valor suportado por Money e validação de códigos ISO 4217 aceitos no MVP.
- TTL/máximo de tentativas e backoff para referências pendentes, além de visibility timeout e tentativas SQS.
- Biblioteca/forma de Unit of Work PostgreSQL e coordenação durável/caching de Redis.
- Catálogo final de `failureCode` e política de autorização para abrir carteiras e consultar wallet/ledger.

## Estratégia de testes

- Meta de cobertura unitária mínima: **90%** dos pacotes de domínio e aplicação, reportada por pacote; excluir apenas código gerado e adapters puramente mecânicos com justificativa documentada. Cobertura não substitui asserts de regras e limites.
- Testes unitários devem cobrir `Money` (`decimal.Decimal`), precisão/escala, moedas incompatíveis, overflow/limites, invariantes de agregados, transições, regras de operação/reversão, idempotência e classificação de erros por `errors.Is()`/`errors.As()`.
- Incluir testes concorrentes com `go test -race`, chamadas paralelas e múltiplas instâncias/conexões independentes. Cobrir a disputa de duas apostas de 80.00 sobre saldo de 100.00, repetição paralela da mesma operação, carteiras diferentes em paralelo e atualização de saldo sem lost update.
- Os testes de integração com PostgreSQL devem validar que a operação SQL atômica com delta decimal impede saldo negativo e lost updates; não depender apenas de mocks para demonstrar essa garantia.
- Critérios e execução dos testes de integração seguem também o README do desafio.

## Referência principal

- [README do desafio Jungle Backend Go](https://github.com/junglegaming/backend-challenge-go)
