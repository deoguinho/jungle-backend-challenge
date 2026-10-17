ARG GO_VERSION=1.26.1
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build --chown=nonroot:nonroot /out/api /app/api
COPY --from=build --chown=nonroot:nonroot /out/worker /app/worker
COPY --from=build --chown=nonroot:nonroot /out/migrate /app/migrate
COPY --from=build --chown=nonroot:nonroot /src/migrations /app/migrations
USER nonroot:nonroot
ENTRYPOINT ["/app/api"]
