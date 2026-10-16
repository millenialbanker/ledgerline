# Multi-stage build of the API server with the PostgreSQL driver.
FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY . .
RUN go get github.com/jackc/pgx/v5@v5.7.2 && go mod tidy \
 && CGO_ENABLED=0 go build -tags pgx -trimpath -ldflags="-s -w" -o /out/ledgerline ./cmd/ledgerline \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ledgerctl ./cmd/ledgerctl

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/ /app/
COPY config /app/config
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/app/ledgerline"]
