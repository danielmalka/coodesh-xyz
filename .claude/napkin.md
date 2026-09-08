# Napkin Runbook

## Curation Rules
- Re-prioritize on every read.
- Keep recurring, high-value notes only.
- Max 10 items per category.
- Each item includes date + "Do instead".

## User Directives
1. **[2026-09-05] Só o operador commita neste challenge**
   Do instead: gerar arquivos, testes e dicas; nunca `git add`, `git commit`, `git push` nem abrir PR. 

2. **[2026-09-05] Entrega precisa parecer autoral, não gerada por LLM**
   Do instead: código curto, nomes em inglês consistente, README em linguagem de gente, commits pequenos. Evitar boilerplate de IA (pastas vazias, comentários óbvios, README inflado).

## Domain Behavior Guardrails
1. **[2026-09-05] Stack: Go stdlib (+ x/time/rate), módulo github.com/danielmalka/coodesh-xyz**
   Do instead: `net/http` + `slog` + channel. Sem framework. Rate limit via `golang.org/x/time/rate` only.

2. **[2026-09-05] PoC Arbitralis/Lexi: receber sync, processar async, responder outbound**
   Do instead: POST `/webhook` responde rápido; LLM simulado (sleep + falha ocasional) fora do request; outbound mock para WhatsApp; estado em memória; não logar PII.

3. **[2026-09-08] Phase 6: two limiters**
   Do instead: inbound `Allow` before body → 429+Retry-After (INBOUND_RPS/BURST); shared upstream bucket for LLM+WhatsApp Wait (UPSTREAM_RPS/BURST, ~2 tokens/job).

4. **[2026-09-05] README do Coodesh tem checklist próprio**
   Do instead: título, descrição em uma frase, stack, install/uso, `.gitignore`, payload de exemplo, ADR (fila prod, retry/DLQ), "This is a challenge by Coodesh".

## Execution & Validation
1. **[2026-09-05] Testes cobrem happy path e falha do LLM**
   Do instead: testes do webhook 202/200 rápido, processamento async, outbound disparado, retry/erro do LLM sem PII no log.
