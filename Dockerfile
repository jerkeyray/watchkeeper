FROM golang:1.27.0-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/watchkeeper-api ./cmd/watchkeeper-api \
    && CGO_ENABLED=0 go build -trimpath -o /out/service-simulator ./cmd/service-simulator \
    && CGO_ENABLED=0 go build -trimpath -o /out/workflow-worker ./cmd/workflow-worker \
    && CGO_ENABLED=0 go build -trimpath -o /out/recovery-coordinator ./cmd/recovery-coordinator \
    && CGO_ENABLED=0 go build -trimpath -o /out/recovery-smoke ./cmd/recovery-smoke \
    && CGO_ENABLED=0 go build -trimpath -o /out/migrate ./cmd/migrate \
    && CGO_ENABLED=0 go build -trimpath -o /out/healthcheck ./cmd/healthcheck

FROM alpine:3.22 AS runtime
RUN addgroup -S watchkeeper && adduser -S -G watchkeeper watchkeeper
WORKDIR /app
COPY --from=build /out/* /app/
COPY migrations /app/migrations
USER watchkeeper
