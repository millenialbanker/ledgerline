.PHONY: kyc test vet sqltest demo run build build-pgx integration fmt

test:            ## unit and end-to-end tests (no database needed)
	go test -race -count=1 ./...

vet:
	gofmt -l . | (! grep .) && go vet ./...

sqltest:         ## schema and store SQL tests against PostgreSQL (PGHOST etc. set)
	db/tests/run.sh

demo:            ## run the sample trading day end to end
	go run ./cmd/ledgerctl demo

run:             ## API server, in-memory stores, demo key
	go run ./cmd/ledgerline -dev

build:
	go build -o bin/ledgerline ./cmd/ledgerline && go build -o bin/ledgerctl ./cmd/ledgerctl && go build -o bin/ledgerline-kyc ./cmd/ledgerline-kyc && go build -o bin/ledgerline-bot ./cmd/ledgerline-bot

kyc:             ## the KYC plugin with its practice DigiLocker
	go run ./cmd/ledgerline-kyc -simulate

build-pgx:       ## server with the PostgreSQL driver
	go get github.com/jackc/pgx/v5@v5.7.2 && go mod tidy && go build -tags pgx -o bin/ledgerline ./cmd/ledgerline

integration:     ## full day against PostgreSQL (LEDGERLINE_TEST_DB, LEDGERLINE_TEST_TENANT)
	go test -tags 'integration pgx' -count=1 ./internal/pgstore

fmt:
	gofmt -w .
