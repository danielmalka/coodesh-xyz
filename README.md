# Lexi Webhook

PoC de um webhook do WhatsApp que responde na hora e deixa o LLM lento para depois: recebe (sync), processa (async) e devolve a resposta por uma chamada outbound.

## Stack

- Go 1.27, biblioteca padrão (`net/http`, `log/slog`, `context`, `sync`, `expvar`, `net/http/pprof`)
- `golang.org/x/time/rate` para os rate limiters (única dependência)
- `golangci-lint` como quality gate

## Como rodar

Requisitos: Go 1.27 ou superior. `golangci-lint` só para o `make lint`.

```bash
make run          # sobe em :8080
make test         # testes
make race         # testes com detector de corrida
make lint         # go vet + golangci-lint
make bench        # benchmarks
```

Sem `make`: `go run ./cmd/server`.

## Payload esperado

```bash
curl -s -X POST localhost:8080/webhook \
  -H 'Content-Type: application/json' \
  -d '{"message_id":"wamid.1","from":"+5511999999999","text":"quero negociar minha dívida"}'
```

```json
{"message_id":"wamid.1","from":"+5511999999999","text":"quero negociar minha dívida"}
```

Resposta imediata, antes de qualquer chamada ao LLM:

```json
{"received":true,"message_id":"wamid.1"}
```

| Status | Quando |
|---|---|
| 202 | aceita e enfileirada, ou duplicada (mesmo `message_id` dentro da janela de dedup) |
| 400 | JSON inválido ou campo vazio |
| 429 | acima do rate limit de entrada, com header `Retry-After` |
| 503 | fila cheia, ou servidor encerrando |

## O fluxo em três etapas

```
Meta ──POST /webhook──▶ handler ──▶ fila em memória ──▶ workers ──▶ LLM (simulado)
         202 em ~µs                                          │
                                                             ▼
                                              POST /mock/whatsapp/messages
                                              (cliente HTTP real, com retry)
```

1. **Receber (sync).** O handler valida o JSON, deduplica por `message_id`, enfileira e responde 202. Nada de LLM dentro da requisição, então a Meta nunca vê timeout.
2. **Processar (async).** Um pool de workers consome a fila. Cada job chama o LLM com timeout por chamada e retry com backoff exponencial e jitter.
3. **Responder (outbound).** O worker faz um POST real para a API de envio do WhatsApp, no formato da Cloud API da Meta. Na PoC o alvo é um endpoint mock no mesmo processo, que loga a entrega e devolve um `wamid`.

Falha definitiva em qualquer etapa vai para uma DLQ (dead-letter queue) em memória, visível em `GET /dlq`.

## Endpoints

| Rota | Função |
|---|---|
| `POST /webhook` | entrada de mensagens |
| `POST /mock/whatsapp/messages` | API de envio simulada (alvo padrão do outbound) |
| `GET /health` | liveness |
| `GET /metrics` | contadores em JSON |
| `GET /dlq` | mensagens que falharam, sem PII |
| `GET /debug/vars` | os mesmos contadores via `expvar` |
| `GET /debug/pprof/` | perfil de CPU, memória e trace |

## Configuração

Tudo por variável de ambiente, com validação na subida.

| Variável | Padrão | O que faz |
|---|---|---|
| `ADDR` | `:8080` | endereço do servidor |
| `WORKERS` | `4` | goroutines consumindo a fila |
| `QUEUE_SIZE` | `256` | capacidade da fila; cheia responde 503 |
| `LLM_TIMEOUT` | `5s` | timeout de cada chamada ao LLM |
| `MAX_ATTEMPTS` | `3` | tentativas por etapa (LLM e outbound) |
| `RETRY_BASE_DELAY` | `200ms` | primeira espera do backoff |
| `RETRY_MAX_DELAY` | `2s` | teto do backoff |
| `INBOUND_RPS` / `INBOUND_BURST` | `50` / `100` | rate limit de entrada no webhook |
| `UPSTREAM_RPS` / `UPSTREAM_BURST` | `10` / `10` | token bucket compartilhado entre LLM e WhatsApp |
| `BREAKER_FAILURES` | `5` | falhas seguidas do LLM para abrir o circuito |
| `BREAKER_COOLDOWN` | `30s` | tempo aberto antes de fazer um probe de novo |
| `WHATSAPP_API_URL` | `http://127.0.0.1:8080/mock/whatsapp/messages` | alvo do outbound |
| `WHATSAPP_TOKEN` | vazio | vira `Authorization: Bearer` quando presente |
| `OUTBOUND_TIMEOUT` | `5s` | timeout de cada tentativa de envio |
| `SHUTDOWN_TIMEOUT` | `10s` | prazo para drenar a fila no shutdown |
| `LLM_MIN_LATENCY` / `LLM_MAX_LATENCY` | `200ms` / `2s` | latência do LLM simulado |
| `LLM_FAILURE_RATE` | `0.15` | fração de chamadas que falham |
| `LLM_SLOW_RATE` | `0.10` | fração que demora 5x o máximo (estoura o timeout) |
| `MOCK_WHATSAPP_FAILURE_RATE` | `0` | fração de 503 no mock, para ver o retry do outbound |

## Roteiro de demonstração

Caminho feliz, com o mock recebendo a resposta:

```bash
make run
scripts/load.sh 100        # noutro terminal
curl -s localhost:8080/metrics
```

LLM fora do ar: tudo cai na DLQ depois de três tentativas.

```bash
LLM_FAILURE_RATE=1 make run
curl -s -X POST localhost:8080/webhook -H 'Content-Type: application/json' \
  -d '{"message_id":"wamid.2","from":"+5511999999999","text":"oi"}'
sleep 3; curl -s localhost:8080/dlq
```

Circuito abrindo: a partir da terceira mensagem o LLM nem é chamado.

```bash
LLM_FAILURE_RATE=1 BREAKER_FAILURES=2 MAX_ATTEMPTS=1 make run
# três POSTs, depois:
curl -s localhost:8080/dlq      # dois "upstream unavailable", um "circuit open"
```

rate limit de entrada:

```bash
INBOUND_RPS=1 INBOUND_BURST=2 make run
# três POSTs seguidos: 202, 202, 429 com Retry-After: 1
```

Graceful shutdown: `Ctrl+C` com jobs na fila. O log mostra `shutting down http`, `closing queue`, `workers drained`. Se o prazo estourar, os jobs que sobraram vão para a DLQ com stage `shutdown`.

## Resiliência

- **Retry com backoff exponencial e full jitter** nas duas etapas. Erros são classificados: 429 e 5xx do WhatsApp são transitórios; outros 4xx são permanentes e param na hora. Timeout por chamada é transitório de propósito: LLM lento é exatamente o caso a repetir.
- **Timeouts em todo lugar:** por chamada ao LLM, por tentativa de envio, no servidor HTTP e no shutdown.
- **Rate limit em dois pontos.** Na entrada, protege a fila e responde 429. No upstream, um único token bucket compartilhado entre LLM e WhatsApp garante que a dependência lenta não receba mais do que aguenta.
- **Circuit breaker no LLM.** Falhas seguidas abrem o circuito; enquanto aberto, o job falha rápido para a DLQ sem tocar no LLM; depois do cooldown um único probe decide se fecha. Cancelamento vindo do shutdown não conta como falha, e respostas atrasadas de chamadas antigas não fecham o circuito.
- **DLQ** com stage, erro, tentativas e horário. Limitada a mil itens.
- **Graceful shutdown em etapas:** para de aceitar, fecha a fila, drena, e só depois do prazo cancela o que está in-flight.
- **Dedup por `message_id`** com janela de dez mil ids, porque a Meta reentrega.
- **Sem PII em log nem em `/dlq`:** telefone aparece como hash, texto nunca aparece. Há teste que captura o log e procura o telefone e o texto.

## Testes e benchmarks

77 testes, 96% de cobertura, todos passando com `-race`. Tudo que envolve tempo (backoff, cooldown do breaker, latência simulada) roda em `testing/synctest`, com clock virtual: segundos de espera viram microssegundos e nenhum teste depende de `Sleep` para acertar timing.

```bash
make bench              # todos os benchmarks
make bench-baseline     # grava 6 rodadas em bench/baseline.txt
make bench-compare      # 6 rodadas novas e comparação com benchstat
make profile-cpu        # pprof do fluxo completo de um job
make profile-live       # pprof do servidor em execução
```

| Benchmark | Resultado (loopback, 8 threads) |
|---|---|
| handler do webhook | ~8 µs/op, 45 allocs |
| handler em paralelo | ~5 µs/op |
| job completo (LLM instantâneo + HTTP outbound) | ~500 µs/op |
| envio HTTP com retry | ~430 µs/op |
| enfileirar com dedup | ~130 ns/op, 0 allocs |
| retry no caminho de sucesso | ~6 ns/op, 0 allocs |
| circuit breaker | ~100 ns/op, 0 allocs |

O perfil de CPU do job completo mostra a maior parte do tempo em syscalls e futex, ou seja, rede e agendamento. O código do pacote não é o gargalo; a dependência externa é, e por isso o desacoplamento resolve o incidente.

## ADR: decisões de arquitetura

### Fila em memória na PoC. E em produção?

A fila é um canal Go com buffer. Serve para a PoC porque é a forma mais simples de provar o desacoplamento, mas perde tudo se o processo cair e não escala além de uma instância.

Em produção eu usaria **Redis Streams** como primeira escolha, ou **NATS JetStream** se já houvesse NATS na casa. Os dois dão persistência, grupos de consumidores com ack explícito, reentrega do que não foi confirmado e visibilidade de pendências, com operação leve. **SQS** é a alternativa gerenciada se a infra for AWS: menos operação, DLQ nativa, mas latência maior e ordem por remetente só com FIFO. **Kafka** seria excesso para este volume: o produto não precisa de replay histórico nem de particionamento pesado, e o custo operacional não se paga.

O que muda no código: a fila vira uma interface com `push` e consumo com ack, o dedup por `message_id` sai do mapa em memória e vai para uma chave com TTL no Redis, e o handler continua igual.

### Retries e DLQ em produção

O retry por tentativa continua no worker, como está: backoff com jitter, timeout por chamada, classificação de erro transitório e permanente. O que muda é o que acontece quando as tentativas acabam.

Com broker, a mensagem que esgotou tentativas vai para uma **DLQ do próprio broker** (stream separado no Redis, stream de DLQ no JetStream, DLQ nativa no SQS), com o motivo e a contagem de tentativas nos metadados. Aí entram três coisas que a PoC não tem: **replay** por operador ou automático depois que o circuito fecha, **alerta** quando a DLQ cresce, e **idempotência no envio**, porque um retry depois de "enviou mas a resposta se perdeu" duplica a mensagem para o cliente. O circuito aberto deixaria de mandar direto para a DLQ e passaria a reagendar com atraso, para não descartar mensagem por uma indisponibilidade curta.

Um cuidado do domínio: mensagem de negociação de dívida que falha precisa de tratamento humano em algum momento. A DLQ não é só técnica, é a fila de atendimento do suporte.

## Escolhas e o que ficou de fora

- **Um pacote só, `internal/lexi`.** É o nome do agente que a PoC simula. São 13 arquivos de produção com menos de 300 linhas cada; dividir em pacotes agora só criaria fronteiras para atravessar.
- **Sem framework web.** O `net/http` do Go 1.22+ tem roteamento por método e padrão. Um framework não traria nada aqui.
- **Rate limit global, não por remetente.** O incidente é sobre o total que a dependência aguenta. Limite por remetente seria um mapa de rate limiters com expiração, e não é o problema descrito.
- **Circuito aberto vai para a DLQ, não reagenda.** Mais simples e mais visível na PoC. A evolução está no ADR.
- **Sem autenticação nos endpoints de operação** (`/dlq`, `/metrics`, `/debug`). Numa PoC local não faz sentido; em produção ficam atrás de rede interna ou auth.
- **Sem assinatura do webhook da Meta** (`X-Hub-Signature-256`). Fora do escopo do enunciado, e seria a primeira coisa a entrar depois.
- **Sem chave de idempotência no envio.** Discutido no ADR.
- **Sem Docker.** Um binário Go sem dependências de sistema não precisa.

## Uso de IA

Usei um assistente de código com IA durante todo o desenvolvimento, e o processo foi o seguinte.

O trabalho foi dividido em nove fases pequenas, cada uma com um contrato explícito: o que construir, o que não tocar, quais testes escrever e qual gate passar. O assistente orquestrava subagentes para implementar cada fase e outros subagentes, com modelos diferentes, para revisar o código gerado. Cada revisão devolvia uma lista de achados com severidade, e cada achado foi aceito ou rejeitado com justificativa antes de virar mudança. Vários foram rejeitados: tratar timeout interno do LLM como erro terminal, validar campos irrelevantes no mock, arquivos de teste "grandes demais".

Antes de qualquer fase chegar a mim, o gate precisava estar verde: `gofumpt`, `go vet`, `golangci-lint` sem avisos, testes com `-race` repetidos três vezes, cobertura acompanhada. O script de carga e o graceful shutdown foram validados contra o servidor rodando, não só por teste unitário.

Toda aprovação final foi minha. Eu revisei cada fase antes de commitar, pedi mudanças (por exemplo, a extração de todos os números mágicos para constantes nomeadas) e alterei manualmente o que achei necessário. O histórico de commits reflete essa cadência.

Padrões que apareceram e que eu mantive porque fazem sentido em Go: erros classificados por wrapper (`permanent`) em vez de por tipo de exceção; `testing/synctest` para tudo que envolve tempo; contexto em toda chamada que pode bloquear; constantes nomeadas para parâmetros e limites, sem inflar com nome para cada string; benchmarks com `b.Loop` e `ReportAllocs` desde o início, para que "rápido" seja um número e não uma opinião.

---

> This is a challenge by [Coodesh](https://coodesh.com/)
