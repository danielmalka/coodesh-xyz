.PHONY: test race cover bench lint run

test:
	go test ./...

race:
	go test -race -count=1 ./...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

bench:
	go test -run '^$$' -bench . -benchmem ./...

lint:
	go vet ./...
	golangci-lint run ./...

run:
	go run ./cmd/server
