# Spec de arquitetura e execução local

## Objetivo e escopo

Este documento registra a arquitetura local do desafio [backend-challenge-go](https://github.com/junglegaming/backend-challenge-go). O desenho foi implementado: Compose inicia as dependências e o provisionador do kind; PostgreSQL, Redis, Keycloak e MiniStack ficam em containers Compose, enquanto API e workers rodam como workloads Kubernetes. O estado implementado e os limites conhecidos estão registrados na seção final.

As decisões de domínio e os contratos abaixo derivam do enunciado do desafio; escolhas de infraestrutura que ele deixa abertas estão marcadas como decisões desta spec. Este documento é evolutivo: decisões confirmadas e alterações serão registradas aqui conforme o projeto for refinado. Decisões ainda sem definição aparecem como pendências, sem antecipar escolhas de implementação.

### Registro de refinamentos

| Data | Alteração | Estado |
|---|---|---|
| 2026-10-16 | Redis definido como caminho de leitura rápida para idempotência, para reduzir carga de leitura no PostgreSQL. Persistência e garantias financeiras movidas para refinamento futuro. | Decisão de cache confirmada; estratégia durável pendente. |
| 2026-10-16 | Money definido com `decimal.Decimal`; schema PostgreSQL usa `NUMERIC` validado para escala 2. Atualização de saldo aplica delta no banco para evitar lost updates. Meta de 90% de testes unitários e verificação de condições de corrida. | Decisões registradas em [`APP_SPEC.md`](APP_SPEC.md) e [`DATABASE_SPEC.md`](DATABASE_SPEC.md). |
| 2026-10-16 | Implementados Compose/kind/MiniStack, Keycloak local, API e workers Fx, adapters pgx/Redis/SQS e migrations `000001`–`000009`. | Ver estado construído ao final deste documento; testes de integração e atomicidade da inbox ainda pendentes. |

## Decisões

### Ambiente local

| Área | Decisão | Motivo |
|---|---|---|
| Cluster | kind com um control-plane e dois workers | Permite validar múltiplas réplicas e concorrência entre processos, conforme o desafio. kind executa nós Kubernetes como containers Docker. |
| Orquestração local | Docker Compose para PostgreSQL, Keycloak, MiniStack e um serviço de provisionamento do kind | Mantém dependências e bootstrap reproduzíveis com `docker compose up`. O provisionador usa o Docker socket do host para criar e configurar o cluster kind. |
| Workloads | API, worker SQS, worker de referências e publisher da outbox como Deployments/Jobs Kubernetes | Exercita o ambiente distribuído real do desafio e mantém a aplicação dentro do cluster. |
| Banco | PostgreSQL em Compose com volume nomeado | Banco relacional escolhido para os dados da aplicação; responsabilidades e garantias serão detalhadas na decisão de persistência. |
| Cache de idempotência | Redis em Compose | Mantém um caminho de leitura rápido para chaves já vistas e reduz consultas repetidas ao PostgreSQL. |
| AWS local | MiniStack em Compose, usando SQS | O desafio exige SQS local e o MiniStack é o emulador escolhido pelo usuário. Usar somente SQS e recursos efetivamente requeridos evita dependência de serviços AWS desnecessários. |
| Identidade | Keycloak em Compose | O desafio recomenda Keycloak e exige OAuth 2.0/OIDC; o serviço publica issuer/JWKS para os workloads. |
| Imagens | Imagem da aplicação construída pelo Docker Compose, carregada no kind antes dos Deployments | Evita registry externo no fluxo local; workloads usam tag versionada local e `imagePullPolicy: IfNotPresent`. |

O provisionador do kind precisa de acesso ao Docker Engine e portanto monta `/var/run/docker.sock`; esse arranjo é apropriado para desenvolvimento local, não para produção. No Docker Desktop, o engine roda dentro da VM do Desktop. O serviço de provisionamento deve ser de execução única e terminar com sucesso após criar o cluster, anexar conectividade e aplicar manifests. A remoção do cluster é explícita (`kind delete cluster`) para não destruir dados ou cluster inadvertidamente ao parar dependências.

### Conectividade

O Compose cria uma rede nomeada e previsível para PostgreSQL, Keycloak, MiniStack e o provisionador. As portas de administração e desenvolvimento são publicadas no host apenas quando necessárias. Os Pods alcançam esses serviços pelo endereço do host/bridge anunciado na configuração local, usando `host.docker.internal` no Docker Desktop; em Linux, o runbook deve fornecer o gateway Docker equivalente. Os endpoints internos de SQS devem ser anunciados de forma roteável pelos Pods, não como `localhost` nem como nome DNS inacessível do container MiniStack.

O cluster kind mapeia uma porta do host para a API HTTP da aplicação (NodePort ou port mapping do kind). O acesso de desenvolvimento usa `http://localhost:<porta>`. Keycloak publica seu issuer em um endereço que seja idêntico ao configurado para validação de `iss` pela API e acessível tanto pelo host quanto pelos Pods; o realm e os clients são inicializados por configuração versionada, sem credenciais reais.

Como o kind cria sua rede Docker própria, o bootstrap deve conectar os containers dos nós kind à rede Compose ou configurar um gateway estável e validado para que Pods alcancem as dependências. Essa etapa é parte obrigatória do provisionamento e deve falhar claramente se a rota não funcionar. A solução não deve depender de IP efêmero escrito manualmente nos manifests.

### Componentes de aplicação

- **API HTTP**: Deployment com pelo menos duas réplicas, endpoints de carteira, transações, consulta, reconciliação e health checks. Cada réplica tem conexões próprias ao PostgreSQL e valida tokens OIDC.
- **Consumidor SQS**: Deployment separado, com concorrência limitada por configuração. Faz long polling, registra inbox e tratamento de domínio na mesma transação SQL, e só apaga a mensagem depois do commit.
- **Worker de referências**: Deployment que retoma `PENDING_REFERENCE` duravelmente com backoff e TTL/máximo de tentativas configurado.
- **Publisher da outbox**: Deployment que disputa registros pendentes com locking por linha (`SKIP LOCKED` ou mecanismo equivalente), publica eventos e confirma envio; `eventId` permanece estável em retries.
- **Migrations**: Job Kubernetes de execução única antes das réplicas da aplicação. Falha de migration impede a implantação dos workloads dependentes.

API, consumidor e workers compartilham os casos de uso de domínio, mas têm processos e ciclo de vida próprios. Uber Fx compõe configuração, conexões, repositórios, handlers e workers e encerra entradas antes de fechar dependências.

### Idempotência

- Redis será o caminho rápido de leitura para chaves de idempotência, reduzindo consultas repetidas ao PostgreSQL e a latência de replays.
- O registro de idempotência conterá a chave, o hash determinístico do conteúdo e os dados do resultado necessários para responder ao replay e detectar reutilização da chave com payload divergente.
- Redis não deve ser tratado como fonte durável única: o desafio exige que idempotência sobreviva à reinicialização dos processos. O registro durável e a coordenação entre Redis e PostgreSQL ficam pendentes para uma decisão posterior sobre persistência e garantias financeiras.
- Política de expiração, invalidação, fallback durante indisponibilidade do Redis, consistência do cache e comportamento após cache miss serão refinados antes da implementação.

### Persistência e garantias financeiras — pendente

O modelo de tabelas e constraints PostgreSQL foi detalhado em [`DATABASE_SPEC.md`](DATABASE_SPEC.md). A escolha da biblioteca de acesso, limites efetivos de transação, implementação da estratégia de concorrência e coordenação operacional entre PostgreSQL e Redis ainda serão refinados antes da implementação. A spec de banco registra as decisões propostas e as invariantes exigidas pelo desafio.

### Mensageria e eventos

- Provisionar `wager-transactions.fifo` e `wager-transactions-dlq.fifo`, com redrive configurado e política FIFO coerente.
- `MessageGroupId` será `walletId`, permitindo ordenação por carteira sem serializar carteiras independentes. `MessageDeduplicationId` será estável por operação externa; deduplicação SQS é otimização, e a persistência SQL segue como garantia.
- O destino dos eventos da outbox será uma fila FIFO local de integração, `wager-events.fifo`, provisionada no MiniStack. Usar uma fila de eventos mantém o publisher independente dos consumidores e permite validar republicação; contratos carregam `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId`, `occurredAt`, `version` e dados tipados.
- A mensagem recebida é removida apenas depois do commit. Erro transitório causa retry e extensão de visibility timeout conforme necessário; mensagens inválidas e falhas permanentes seguem a política de tentativas e DLQ documentada na configuração. `SIGTERM` pausa polling e dá prazo ao processamento corrente.
- SQS é at-least-once. Publicação confirmada seguida de falha ao marcar outbox pode duplicar evento, logo consumidores de eventos devem deduplicar pelo `eventId`.

### Segurança e configuração

- Keycloak fornece tokens OAuth 2.0/OIDC. Para comunicação serviço a serviço usar `client_credentials`, com clients e roles mínimos distintos para API/worker quando necessário.
- `providerId` autorizado é derivado das claims/roles do token validado; não confiar no identificador livre informado pelo cliente. Providers só consultam suas próprias transações. Operações internas de carteira não ficam expostas como chamadas de provider.
- Acesso SQS usa credenciais locais fictícias e policy mínima se suportada pelo emulador. Nenhuma credencial AWS real é necessária no ambiente local.
- Configuração é injetada por variáveis de ambiente/Secrets Kubernetes geradas localmente. Arquivos de exemplo contêm valores fictícios; segredos reais e arquivos locais não entram no controle de versão.

### Observabilidade e saúde

- Logs estruturados JSON com `correlationId`, `messageId`, `transactionId`, `walletId` e `providerId` quando disponíveis; não registrar tokens, segredos nem payload financeiro integral.
- `GET /health/live` verifica processo; `GET /health/ready` verifica PostgreSQL e SQS. Readiness falha quando dependências essenciais estão indisponíveis, sem reiniciar o processo por uma falha transitória.
- Métricas incluem resultados por status, duplicatas, retries, DLQ, conflitos de concorrência, atraso da outbox, latência e divergências de reconciliação. Métricas e tracing são opcionais no stack local inicial; logs e health checks são necessários.

## Topologia

```text
Host
└── Docker Compose
    ├── PostgreSQL + volume
    ├── Redis + volume/configuração de persistência a definir
    ├── Keycloak + volume de dados local opcional
    ├── MiniStack (SQS) + persistência local opcional
    └── kind-bootstrap (Docker socket)
        └── kind: control-plane + 2 workers
            ├── migration Job
            ├── API Deployment (>= 2 réplicas)
            ├── SQS consumer Deployment
            ├── reference worker Deployment
            └── outbox publisher Deployment
```

## Passo a passo de execução

Os nomes abaixo são comandos-alvo para a futura implementação da infraestrutura; esta spec não cria scripts nem arquivos Compose/Kubernetes.

1. **Pré-requisitos**: instalar Docker Engine/Desktop com Compose v2, `kubectl`, `kind` e AWS CLI (ou cliente compatível). No Windows, iniciar Docker Desktop com Kubernetes desativado ou sem conflito de portas. Reservar memória suficiente para PostgreSQL, Keycloak, MiniStack e três nós kind.
2. **Preparar configuração local**: copiar o arquivo de exemplo de ambiente para configuração local; definir portas, realm/client OIDC, issuer, região AWS local, endpoint SQS acessível pelos Pods e credenciais fictícias. Não preencher credenciais AWS reais.
3. **Subir dependências e construir imagem**: `docker compose up -d --build postgres redis keycloak ministack`. Aguardar health checks de PostgreSQL, Redis, Keycloak e `http://localhost:4566/_ministack/health`.
4. **Provisionar AWS local**: executar os scripts de inicialização do MiniStack para criar as filas de entrada, DLQ e eventos; confirmar atributos FIFO, redrive e URLs/endpoints alcançáveis pelos Pods.
5. **Preparar identidade**: inicializar realm, clients de serviço, roles e mapeamento do provider por configuração importável. Confirmar discovery document e JWKS pelo endereço configurado no issuer.
6. **Criar cluster**: `docker compose run --rm kind-bootstrap`. O serviço cria cluster kind com control-plane e dois workers, associa conectividade à rede das dependências, carrega a imagem da aplicação, configura acesso HTTP local e instala namespace/configuração necessária. Reexecução deve ser idempotente ou informar como remover o cluster existente.
7. **Aplicar schema**: aplicar manifests base e executar o Job de migrations; verificar status e logs. Não iniciar Deployments de aplicação se a migration falhar.
8. **Subir workloads**: aplicar Deployments e Services; acompanhar `kubectl rollout status` para API, consumidor e workers. Confirmar duas ou mais réplicas da API e readiness verde.
9. **Validar operação**: consultar `/health/live` e `/health/ready` via porta local; autenticar via client credentials; criar carteira e submeter operações; verificar transações, ledger, inbox/outbox e mensagens no MiniStack. Exercitar reenvio idempotente e observar logs correlacionados.
10. **Parar ambiente**: `kubectl delete` dos workloads/manifests ou `kind delete cluster --name <nome>` para remover o cluster. `docker compose down` para parar serviços. Preservar volumes em uso; para reset completo e destrutivo dos dados locais, documentar um comando separado e intencional com remoção de volumes.

## Critérios de aceite da futura implementação

- Um comando Compose constrói e inicia dependências e provisionador; a execução descrita provisiona o cluster sem registry externo.
- Pelo menos três processos de aplicação independentes conseguem operar com suas próprias conexões/memória.
- O cenário concorrente de duas apostas de 80.00 BRL sobre saldo 100.00 BRL termina com uma aposta processada, uma rejeitada, saldo 20.00 BRL e um débito no ledger.
- A implementação deverá comprovar que reinícios de Pods não perdem idempotência; a fonte durável e a recuperação do cache serão definidas junto com a decisão de persistência.
- Mensagens só são removidas após resultado durável; eventos são publicados via outbox e podem ser deduplicados pelo identificador estável.
- Migrations, endpoints, autenticação por provider e encerramento gracioso têm instruções reproduzíveis.

## Referências

- [Enunciado do desafio](https://github.com/junglegaming/backend-challenge-go)
- [kind Quick Start](https://kind.sigs.k8s.io/docs/user/quick-start/) — cluster Kubernetes local baseado em containers Docker; imagens locais podem ser carregadas no cluster.
- [MiniStack](https://github.com/ministackorg/ministack) — emulador AWS local, endpoint padrão na porta 4566, SQS e scripts de inicialização. Fixar uma versão/tag revisada na implementação, em vez de depender de `latest`.

## Estado da implementacao

A topologia descrita acima esta materializada em `compose.yaml`, `deploy/kind/`, `deploy/kubernetes/`, `deploy/keycloak/` e `deploy/ministack/`. O Compose define PostgreSQL 16, Redis 7, Keycloak 26 e MiniStack; o bootstrap cria/reutiliza um cluster kind com control-plane e dois workers, conecta os nos a rede local, gera ConfigMap/Secret, constroi/carrega a imagem, executa migrations e aplica workloads. A API fica publicada em `http://localhost:8080`.

Os binarios `cmd/api`, `cmd/worker` e `cmd/migrate` usam Uber Fx. Os adapters concretos usam `pgx`, Redis e AWS SDK SQS. Migrations `000001` a `000009` incluem o agendamento/expiracao das referencias. Redis acelera replays; PostgreSQL permanece autoritativo. A fila principal, DLQ e fila FIFO de eventos sao inicializadas em `deploy/ministack/init-sqs.sh`.

Validacao realizada nesta iteracao: `docker compose config --quiet`, `go test ./...` e `go vet ./...` passaram. Os testes de dominio reportam 92.1% a 97.9%; `internal/application` reporta 3.6%, abaixo da meta de 90%. `go test -race ./...` nao foi executado com sucesso neste ambiente Windows porque nao ha compilador C (gcc) disponivel para CGO. O ambiente Docker/kind e as migrations contra PostgreSQL real ainda nao foram iniciados para teste integrado.

Limites ainda abertos: a inbox e concluida em transacoes separadas da transacao financeira; a idempotencia PostgreSQL evita reaplicacao apos falha, mas o fluxo nao satisfaz atomicidade inbox + dominio descrita no README de referencia. A reconciliacao le carteira e ledger em consultas separadas e deve usar um snapshot consistente. A implementacao nao deve ser considerada como cobrindo todos os criterios de aceite antes da conclusao desses itens e dos testes de integracao, concorrencia e falha.