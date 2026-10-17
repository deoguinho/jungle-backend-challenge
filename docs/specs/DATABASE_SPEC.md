# Spec de banco de dados

## Objetivo

Definir o schema PostgreSQL e a estratégia de migrations para os requisitos do [README do desafio](https://github.com/junglegaming/backend-challenge-go). Este é um documento de especificação; não contém migrations executáveis.

O schema protege integridade e auditoria no banco. Redis continua sendo o caminho rápido de leitura da idempotência, conforme [`ARCHITECTURE.md`](../../ARCHITECTURE.md), mas não é a única cópia: o registro durável no PostgreSQL é necessário para sobreviver à perda ou reinicialização do cache e dos processos.

## Convenções

- Schema dedicado `app`; objetos de controle de migration podem ficar em `public` ou no schema padrão exigido pela ferramenta escolhida.
- PostgreSQL, SQL explícito e `pgx` são a decisão recomendada; manter migrations independentes de ORM.
- IDs são UUID gerados pela aplicação (UUIDv7 recomendado) e nunca reutilizados. FKs usam `ON DELETE RESTRICT`; dados financeiros não são apagados em cascata.
- Timestamps são `TIMESTAMPTZ` em UTC. Valores monetários usam domínio PostgreSQL `app.money_amount` baseado em `NUMERIC`, com máximo de 18 dígitos inteiros e escala máxima de 2, mais moeda `CHAR(3)` ISO 4217. Assim, o banco rejeita precisão excedente em vez de arredondá-la silenciosamente. A aplicação usa `decimal.Decimal` (`github.com/shopspring/decimal`) e contratos externos representam valores como strings decimais com duas casas; `float32`/`float64` não são usados em parsing, cálculo, serialização ou persistência.
- O domínio `app.money_amount` deve validar `VALUE = trunc(VALUE, 2)`, `scale(VALUE) <= 2` e `abs(VALUE) < 1000000000000000000`; validar escala antes de qualquer coerção para `NUMERIC(p,2)`, que poderia arredondar silenciosamente.
- Estados e tipos são `TEXT` com `CHECK`, em vez de ENUM PostgreSQL, para facilitar evolução versionada.
- Colunas de payload/evento são `JSONB`; JSONB não substitui colunas tipadas usadas em chaves, constraints ou consultas.
- Colunas obrigatórias são `NOT NULL`; constraints também rejeitam strings vazias quando aplicável.

## Modelo relacional

```text
wallets 1 ─── N wager_transactions 1 ─── 0..1 wallet_ledger_entries
                    │                         │
                    ├── self reference         └── wallet
                    ├── 1 ─── N outbox_events
                    └── 1 ─── 0..1 idempotency_records

inbox_messages (consumer_name, message_id) é independente e deduplica entregas SQS.
```

## Tabelas

### `wallets`

| Coluna | Tipo | Regras |
|---|---|---|
| `id` | UUID | PK |
| `player_id` | UUID | NOT NULL |
| `currency` | CHAR(3) | NOT NULL; ISO 4217 |
| `balance` | `app.money_amount` | NOT NULL, `CHECK (balance >= 0)` |
| `version` | BIGINT | NOT NULL, `CHECK (version >= 1)`, inicia em 1 |
| `created_at`, `updated_at` | TIMESTAMPTZ | NOT NULL |

Unique `(player_id, currency)` impede carteiras duplicadas por jogador e moeda. Uma chave única composta `(id, player_id, currency)` permite FKs compostas nas transações e garante que a moeda/jogador referenciados correspondem à carteira.

### `wager_transactions`

Registra tanto operações externas quanto a abertura interna da carteira (`OPENING`). A coluna `source` distingue `EXTERNAL` e `INTERNAL`.

| Coluna | Tipo | Regras |
|---|---|---|
| `id` | UUID | PK |
| `source` | TEXT | `EXTERNAL` ou `INTERNAL` |
| `kind` | TEXT | `OPENING`, `BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK` |
| `status` | TEXT | `PENDING`, `PENDING_REFERENCE`, `PROCESSED`, `REJECTED`, `FAILED` |
| `provider_id` | TEXT | externo obrigatório; interno nulo |
| `external_transaction_id` | TEXT | externo obrigatório; interno nulo |
| `idempotency_key` | TEXT | externo obrigatório; interno nulo |
| `payload_hash` | BYTEA | externo obrigatório; tamanho fixo definido pelo algoritmo de hash |
| `wallet_id`, `player_id`, `currency` | UUID, UUID, CHAR(3) | NOT NULL; FK composta com `wallets` |
| `round_id`, `game_id` | TEXT | obrigatórios para externas; nulos em `OPENING` |
| `amount` | `app.money_amount` | NOT NULL, `CHECK (amount >= 0)` |
| `reference_external_transaction_id` | TEXT | nulo para operação sem referência; obrigatório em `REFUND`/`ROLLBACK` |
| `reference_transaction_id` | UUID | FK para `wager_transactions(id)`, resolvida quando referência conhecida |
| `failure_code` | TEXT | nulo salvo rejeição/falha; código estável |
| `result_balance` | `app.money_amount` | snapshot da resposta original quando a operação terminou |
| `created_at`, `updated_at`, `completed_at` | TIMESTAMPTZ | `created_at`/`updated_at` NOT NULL; `completed_at` terminal |

Constraints e índices:

- `CHECK` de `source`: `EXTERNAL` só admite os cinco tipos externos; `INTERNAL` só admite `OPENING`.
- `CHECK` de formato por origem: registros externos têm provider, ID externo, chave/hash, rodada/jogo; registros internos não podem ter esses metadados.
- `CHECK` de valor: `LOSS` aceita zero; `OPENING` aceita zero ou positivo; `BET`, `WIN`, `REFUND` e `ROLLBACK` exigem valor maior que zero.
- `CHECK` de referência: `REFUND` e `ROLLBACK` requerem identificador de referência; tipos sem referência permitida não a recebem. `OPENING` não aceita referência.
- `UNIQUE (provider_id, external_transaction_id)` impede reaplicar uma operação externa com outra chave.
- `UNIQUE (provider_id, idempotency_key)` impede reutilizar chave para operações distintas do mesmo provider.
- Índice único parcial em `wallet_id` para `source = 'INTERNAL' AND kind = 'OPENING'` impede abertura duplicada por carteira.
- Índices de consulta em `(wallet_id, created_at, id)`, `(provider_id, external_transaction_id)`, `(status, created_at)` e referências pendentes.
- Uma referência resolvida deve apontar para operação do mesmo provider, jogador, carteira, moeda e rodada. Como isso envolve duas linhas, validar com trigger PostgreSQL ou rotina transacional com lock e testes; um `CHECK` simples não pode garantir essa relação.
- Transações terminais não podem voltar a estado não terminal nem mudar seu resultado. Trigger de transição restringe mudanças a partir de `PROCESSED`, `REJECTED` ou `FAILED`.

### `wallet_ledger_entries`

Um registro para cada alteração efetiva de saldo, incluindo crédito inicial positivo (`OPENING`). `LOSS`, operação rejeitada e abertura com saldo zero não geram lançamento.

| Coluna | Tipo | Regras |
|---|---|---|
| `id` | UUID | PK |
| `wallet_id` | UUID | NOT NULL, FK `wallets` |
| `transaction_id` | UUID | NOT NULL, FK `wager_transactions` |
| `direction` | TEXT | `DEBIT` ou `CREDIT` |
| `amount` | `app.money_amount` | NOT NULL, `CHECK (amount > 0)` |
| `balance_before` | `app.money_amount` | NOT NULL, não negativo |
| `balance_after` | `app.money_amount` | NOT NULL, não negativo |
| `created_at` | TIMESTAMPTZ | NOT NULL |

Constraints:

- `UNIQUE (wallet_id, transaction_id)` impede mais de um lançamento por operação/carteira.
- `CHECK`: crédito requer `balance_after = balance_before + amount`; débito requer `balance_before >= amount` e `balance_after = balance_before - amount`.
- FK composta `(wallet_id, transaction_id)` deve garantir que a transação pertence à mesma carteira (com chave única correspondente em `wager_transactions`).
- Trigger rejeita `UPDATE` e `DELETE`. O role de runtime recebe apenas `SELECT` e `INSERT` nesta tabela; migrations usam role separado.
- Garantir que todo lançamento e sua transação correspondente sejam consistentes no mesmo commit. Regras que comparam o status/tipo da transação exigem trigger de constraint diferível ou escrita transacional validada; documentar e testar a escolha na implementação.

### `idempotency_records`

Registro durável para além do cache Redis.

| Coluna | Tipo | Regras |
|---|---|---|
| `id` | UUID | PK |
| `provider_id` | TEXT | NOT NULL |
| `idempotency_key` | TEXT | NOT NULL |
| `payload_hash` | BYTEA | NOT NULL |
| `transaction_id` | UUID | NOT NULL, FK única para `wager_transactions` |
| `response_snapshot` | JSONB | NOT NULL; resultado original para replay |
| `created_at`, `updated_at` | TIMESTAMPTZ | NOT NULL |

`UNIQUE (provider_id, idempotency_key)` é a garantia definitiva contra reutilização. Hash igual permite replay; hash diferente resulta em conflito. O snapshot preserva o saldo retornado no processamento original. A gravação deste registro e da transação correspondente deve ocorrer no mesmo limite transacional. Redis contém uma cópia aceleradora, nunca a única cópia confirmada.

### `inbox_messages`

| Coluna | Tipo | Regras |
|---|---|---|
| `consumer_name` | TEXT | NOT NULL |
| `message_id` | TEXT | NOT NULL |
| `payload_hash` | BYTEA | NOT NULL |
| `received_at` | TIMESTAMPTZ | NOT NULL |
| `completed_at` | TIMESTAMPTZ | nulo até tratamento durável |
| `transaction_id` | UUID | FK opcional para transação processada |
| `failure_code` | TEXT | opcional para resultado terminal inválido/rejeitado |

PK `(consumer_name, message_id)`. Reentrega com mesmo ID e hash igual é duplicata; mesmo ID com hash divergente é anomalia a registrar e rejeitar. O registro de conclusão e os efeitos de domínio devem compartilhar a transação SQL. Mensagem inválida pode ser marcada terminal segundo a política da DLQ.

### `outbox_events`

| Coluna | Tipo | Regras |
|---|---|---|
| `event_id` | UUID | PK estável em retries |
| `aggregate_id` | UUID | NOT NULL |
| `event_type` | TEXT | NOT NULL |
| `event_version` | INTEGER | NOT NULL, `CHECK (event_version > 0)` |
| `correlation_id` | UUID | NOT NULL |
| `causation_id` | UUID | opcional |
| `occurred_at` | TIMESTAMPTZ | NOT NULL |
| `payload` | JSONB | NOT NULL; snapshot imutável |
| `attempt_count` | INTEGER | NOT NULL DEFAULT 0, não negativo |
| `next_attempt_at` | TIMESTAMPTZ | NOT NULL |
| `published_at` | TIMESTAMPTZ | nulo até confirmação |
| `locked_at`, `locked_by` | TIMESTAMPTZ, TEXT | lease opcional para publishers concorrentes |
| `last_error` | TEXT | opcional; sem credenciais/dados sensíveis |
| `created_at` | TIMESTAMPTZ | NOT NULL |

FK lógica para agregado/transação conforme evento. Índice parcial de pendências `(next_attempt_at, created_at)` onde `published_at IS NULL`; aquisição concorrente por `FOR UPDATE SKIP LOCKED` ou lease equivalente. Atualizações só alteram campos de entrega; `event_id`, tipo, versão, ocorrência e payload ficam imutáveis após inserção (trigger). A inserção da outbox ocorre no mesmo commit dos dados que originaram o evento.

## Invariantes que precisam de proteção no banco

1. Saldo da carteira nunca negativo (`CHECK`).
2. Uma carteira por `(player_id, currency)` (`UNIQUE`).
3. Uma operação externa por `(provider_id, external_transaction_id)` e uma chave por `(provider_id, idempotency_key)` (`UNIQUE`).
4. Um lançamento por `(wallet_id, transaction_id)` e cálculo coerente de saldo anterior/posterior (`UNIQUE` + `CHECK`).
5. Ledger append-only por trigger e privilégios SQL, não apenas convenção da aplicação.
6. Uma abertura interna (`OPENING`) por carteira (índice único parcial); origem externa não pode enviar `OPENING` (`CHECK`).
7. Referências financeiras válidas e reversão única: ao menos uma reversão processada por transação referenciada. Materializar a regra com índice único parcial sobre `reference_transaction_id` para `REFUND`/`ROLLBACK` bem-sucedidos, mais lock/validação transacional. Isso adota a política de que não se reembolsa e também se reverte a mesma aposta diretamente; um `ROLLBACK` de um `REFUND` aponta para a própria transação `REFUND` e permanece possível.
8. Estado terminal e payload/evento não sofrem mutação indevida (triggers e grants mínimos).
9. Constraints compostas/FKs e validações transacionais impedem carteira, jogador, moeda ou provider divergentes entre operação, referência e ledger.

### Atualização de saldo sem lost updates

Não ler o saldo, calcular em Go e gravar um valor absoluto baseado em leitura possivelmente desatualizada. Aplicar o delta diretamente ao valor corrente no PostgreSQL, em uma instrução atômica com parâmetro decimal/string tipado como `NUMERIC`, por exemplo conceitualmente:

```sql
UPDATE app.wallets
SET balance = balance + $1::numeric,
    version = version + CASE WHEN $1::numeric <> 0 THEN 1 ELSE 0 END,
    updated_at = now()
WHERE id = $2
  AND balance + $1::numeric >= 0
RETURNING balance, version;
```

O exemplo descreve a regra e não é migration pronta. O delta deve ser convertido/validado como decimal exato e ter no máximo duas casas. A instrução condicional evita saldo negativo; PostgreSQL serializa concorrência na linha da carteira e avalia a condição sobre o valor corrente após espera pelo lock, prevenindo lost updates sem lock global. `LOSS` não executa alteração de saldo/versão. A transação SQL deve também inserir o ledger com `balance_before`/`balance_after`, a operação, idempotência e outbox conforme aplicável. Nenhuma linha retornada exige distinguir carteira inexistente, saldo insuficiente ou conflito/retry por estratégia transacional explícita.

## Índices adicionais

- `wallets (player_id, currency)` unique.
- `wager_transactions (provider_id, external_transaction_id)` unique e `(provider_id, idempotency_key)` unique.
- `wager_transactions (wallet_id, created_at DESC, id DESC)` para consultas paginadas estáveis.
- `wager_transactions (provider_id, created_at DESC, id DESC)` e índice por `reference_transaction_id`.
- Índice parcial em pendências por `(status, updated_at)` para `PENDING` e `PENDING_REFERENCE`.
- `wallet_ledger_entries (wallet_id, created_at, id)` para cursor estável.
- `outbox_events (next_attempt_at, created_at)` parcial para eventos não publicados.
- `inbox_messages (completed_at)` apenas se a rotina de limpeza/observabilidade necessitar; a PK já cobre deduplicação.

Evitar índices redundantes: validar índices pelo plano das consultas antes de adicionar outros.

## Migrations

Usar migrations SQL versionadas, com um arquivo `up` e um `down` por versão, registradas em tabela de controle do `golang-migrate`. O comando `cmd/migrate` executa o processo explicitamente; o Job Kubernetes roda antes dos workloads. A ferramenta mantém a serialização de migrations com lock do banco, e falha interrompe o Job.

| Ordem | Migration | Conteúdo |
|---|---|---|
| 001 | `create_app_schema` | Schema `app`, domínio `money_amount` com `NUMERIC`, escala máxima 2 e limite de magnitude, e funções utilitárias/triggers mínimos |
| 002 | `create_wallets` | Carteiras, PK/FKs/unique e checks de moeda, saldo, versão |
| 003 | `create_wager_transactions` | Operações, estados, origem, referências, checks e índices |
| 004 | `create_wallet_ledger_entries` | Ledger, unicidade, equação do saldo, triggers de imutabilidade e grants |
| 005 | `create_idempotency_records` | Registro durável, unique de chave e FK para transação |
| 006 | `create_inbox_messages` | Inbox e PK de deduplicação |
| 007 | `create_outbox_events` | Outbox, payload versionado, lease, índices e proteção de campos imutáveis |
| 008 | `add_reversal_and_cross_record_guards` | Índices únicos parciais e triggers/constraints diferíveis para referências e estado terminal |
| 009 | `reference_retry_schedule` | Tentativas, próximo horário, TTL de 24 horas e índice para retomada de referências pendentes |

### Regras de evolução e rollback

- Cada migration deve ser pequena, determinística e transacional sempre que o PostgreSQL permitir.
- Mudanças incompatíveis seguem expand/contract: adicionar coluna/estrutura compatível, implantar código que suporte ambas, migrar dados e somente depois remover o formato antigo.
- Rollback documentado deve ser executável antes da próxima versão e não pode apagar lançamentos financeiros. Se uma migration já recebeu dados de negócio ou se o downgrade implicar perda/auditoria, o `down` deve falhar com mensagem clara; corrigir por migration forward-only.
- Índices grandes em tabelas existentes podem exigir `CREATE INDEX CONCURRENTLY`, fora de uma transação, com tratamento documentado de falha parcial.
- O Job de migration executa com credencial privilegiada separada. Usuário da aplicação não é dono das tabelas e não pode desativar triggers nem alterar/excluir ledger.
- Não executar migrations automaticamente em cada réplica da API; executar uma única vez no fluxo local/CI/deploy antes dos workloads.

## Testes unitários e condições de corrida

- Meta do projeto: cobertura mínima de **90%** nos pacotes de domínio e aplicação, medida por pacote. Excluir código gerado ou adapters triviais somente com justificativa; a cobertura não substitui casos de borda/asserts de invariantes.
- Exercitar parsing e aritmética de `decimal.Decimal`, escala/precisão, moedas incompatíveis, valores fora do limite de `app.money_amount`, agregados, transições, reversões e erros tipados.
- Executar `go test -race` nos testes aplicáveis e criar cenários com operações concorrentes, incluindo 50 reenvios da mesma aposta, duas apostas de 80.00 contra saldo 100.00, e carteiras independentes em paralelo.
- Testes de integração com PostgreSQL real devem disputar a mesma carteira por conexões/processos independentes e demonstrar que a atualização por delta não perde saldo, não cria saldo negativo e mantém o ledger coerente. Mocks unitários não são evidência suficiente dessas garantias.

## Critérios de verificação do schema

- Migrations sobem em banco vazio e rollback segue a política acima.
- Inserções inválidas são rejeitadas pelo próprio PostgreSQL: saldo negativo, escala monetária inválida no contrato de entrada (validação de domínio), duplicidade de carteira/operação/chave/ledger, metadados externos em `OPENING`, tipo e status desconhecidos, transição terminal inválida e edição/exclusão do ledger.
- Referências de reversão incompatíveis ou duplicadas não conseguem produzir duas reversões confirmadas.
- Repetição equivalente recupera resultado original; chave ou ID externo reutilizado com hash divergente retorna conflito sem movimento adicional.
- Concorrência de operações na mesma carteira não gera saldo negativo nem lost update; carteiras diferentes não são serializadas por lock global.
- Inbox e outbox preservam retomada após reinício, e a outbox mantém `event_id` em publicação repetida.

## Requisitos do desafio cobertos

Esta modelagem traduz para PostgreSQL os requisitos de dinheiro exato, idempotência persistente, garantias no banco, ledger append-only, concorrência por carteira, proteção contra lost updates, abertura interna, referências/reversões, inbox/outbox e migrations versionadas e reversíveis documentados no README do desafio.
