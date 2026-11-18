.PHONY: build fmt fmt-check vet test race integration integration-race check migrate up down logs proto openapi clean

build:
	mkdir -p bin
	go build -trimpath -o bin/controller ./cmd/controller
	go build -trimpath -o bin/simulator ./cmd/simulator
	go build -trimpath -o bin/migrate ./cmd/migrate

fmt:
	go fmt ./...

fmt-check:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './.git/*'))" || (gofmt -l $$(find . -name '*.go' -not -path './.git/*'); exit 1)

vet:
	go vet ./...

test:
	go test ./...

race:
	go test -race ./...

integration:
	@test -n "$${TEST_DATABASE_URL:-}" || (echo 'Set TEST_DATABASE_URL to a separate labreserve_test database'; exit 1)
	go test -tags=integration -count=1 ./...

integration-race:
	@test -n "$${TEST_DATABASE_URL:-}" || (echo 'Set TEST_DATABASE_URL to a separate labreserve_test database'; exit 1)
	go test -race -tags=integration -count=1 ./...

check: fmt-check vet build test race openapi

migrate:
	go run ./cmd/migrate

up:
	docker compose up --build -d

down:
	docker compose down

logs:
	docker compose logs -f controller simulator

proto:
	sh scripts/generate-proto.sh

openapi:
	sh scripts/validate-openapi.sh

clean:
	rm -rf bin coverage
