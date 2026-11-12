.PHONY: kyc test vet sqltest demo run build build-pgx integration fmt migrate-tool

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
	go build -o bin/ledgerline ./cmd/ledgerline && go build -o bin/ledgerctl ./cmd/ledgerctl && go build -o bin/ledgerline-kyc ./cmd/ledgerline-kyc && go build -o bin/ledgerline-bot ./cmd/ledgerline-bot && go build -o bin/ledgerline-control ./cmd/ledgerline-control

kyc:             ## the KYC plugin with its practice DigiLocker
	go run ./cmd/ledgerline-kyc -simulate

build-pgx:       ## server with the PostgreSQL driver
	go get github.com/jackc/pgx/v5@v5.7.2 && go mod tidy && go build -tags pgx -o bin/ledgerline ./cmd/ledgerline

integration:     ## full day against PostgreSQL (LEDGERLINE_TEST_DB, LEDGERLINE_TEST_TENANT)
	go test -tags 'integration pgx' -count=1 ./internal/pgstore

fmt:
	gofmt -w .

# ---- migration tool (runs at the broker's office; see cmd/ledgerline-migrate) ----
MIGRATE_VERSION ?= 1.0
MIGRATE_FLAGS = -trimpath -ldflags="-s -w -X ledgerline/internal/migrate/app.Version=$(MIGRATE_VERSION)"
MIGRATE_PKG = ./cmd/ledgerline-migrate

migrate-tool:    ## the migration tool for Windows, macOS (plain and .app) and Linux, in bin/
	mkdir -p bin
	GOOS=windows GOARCH=amd64 go build $(MIGRATE_FLAGS) -o bin/ledgerline-migrate-windows-amd64.exe $(MIGRATE_PKG)
	GOOS=darwin GOARCH=arm64 go build $(MIGRATE_FLAGS) -o bin/ledgerline-migrate-darwin-arm64 $(MIGRATE_PKG)
	GOOS=darwin GOARCH=amd64 go build $(MIGRATE_FLAGS) -o bin/ledgerline-migrate-darwin-amd64 $(MIGRATE_PKG)
	GOOS=linux GOARCH=amd64 go build $(MIGRATE_FLAGS) -o bin/ledgerline-migrate-linux-amd64 $(MIGRATE_PKG)
	go run ./internal/migrate/mkapp -version $(MIGRATE_VERSION) \
		-arm64 bin/ledgerline-migrate-darwin-arm64 -amd64 bin/ledgerline-migrate-darwin-amd64 \
		-out "bin/Ledgerline Migration.app" -zip "bin/Ledgerline-Migration-macOS.zip"
	# TODO signing and notarisation (not done yet; both builds are unsigned):
	#  Windows: when WINDOWS_CERT_PFX and WINDOWS_CERT_PASSWORD are set, sign
	#    bin/ledgerline-migrate-windows-amd64.exe (signtool or osslsigncode, with a timestamp server).
	#  macOS: when APPLE_DEVELOPER_ID ("Developer ID Application: ...") and
	#    APPLE_NOTARY_PROFILE (a notarytool keychain profile) are set,
	#    codesign --options runtime --timestamp -s "$$APPLE_DEVELOPER_ID" "bin/Ledgerline Migration.app",
	#    xcrun notarytool submit --wait, then xcrun stapler staple, and zip again.
