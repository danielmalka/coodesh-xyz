.PHONY: test race cover bench lint run bench-baseline bench-compare profile-cpu profile-mem profile-live load

SHELL := /usr/bin/env bash
.SHELLFLAGS := -o pipefail -ec

BENCH_DIR ?= bench
BENCH_COUNT ?= 6

test:
	go test ./...

race:
	go test -race -count=1 ./...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

bench:
	go test -run '^$$' -bench . -benchmem ./...

bench-baseline:
	mkdir -p $(BENCH_DIR)
	go test -run '^$$' -bench . -benchmem -count=$(BENCH_COUNT) ./... | tee $(BENCH_DIR)/baseline.txt

bench-compare:
	mkdir -p $(BENCH_DIR)
	go test -run '^$$' -bench . -benchmem -count=$(BENCH_COUNT) ./... | tee $(BENCH_DIR)/current.txt
	go run golang.org/x/perf/cmd/benchstat@latest $(BENCH_DIR)/baseline.txt $(BENCH_DIR)/current.txt

profile-cpu:
	mkdir -p $(BENCH_DIR)
	go test -run '^$$' -bench BenchmarkProcessJob -benchmem -cpuprofile $(BENCH_DIR)/cpu.prof -o $(BENCH_DIR)/lexi.test ./internal/lexi/
	go tool pprof -top -nodecount=15 $(BENCH_DIR)/lexi.test $(BENCH_DIR)/cpu.prof

profile-mem:
	mkdir -p $(BENCH_DIR)
	go test -run '^$$' -bench BenchmarkProcessJob -benchmem -memprofile $(BENCH_DIR)/mem.prof -o $(BENCH_DIR)/lexi.test ./internal/lexi/
	go tool pprof -top -nodecount=15 -sample_index=alloc_space $(BENCH_DIR)/lexi.test $(BENCH_DIR)/mem.prof

profile-live:
	go tool pprof -top -nodecount=15 http://localhost:8080/debug/pprof/profile?seconds=10

load:
	./scripts/load.sh

lint:
	go vet ./...
	golangci-lint run ./...

run:
	go run ./cmd/server
