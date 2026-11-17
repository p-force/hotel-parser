FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /hotel-parser ./cmd/hotel-parser
RUN CGO_ENABLED=0 GOOS=linux go build -o /collection ./cmd/collection

FROM alpine:3.22

RUN apk add --no-cache tzdata ca-certificates

ENV HOTEL_PARSER_LOG_FORMAT=json

WORKDIR /app

COPY --from=builder /hotel-parser /app/hotel-parser
COPY --from=builder /collection /app/collection
COPY --from=builder /app/configs /app/configs
COPY deploy/collection.crontab deploy/collection-cron.sh deploy/collection-entrypoint.sh /app/deploy/
RUN cp /app/deploy/collection.crontab /etc/crontabs/root \
    && cp /app/deploy/collection-cron.sh /app/collection-cron \
    && chmod 600 /etc/crontabs/root \
    && chmod 755 /app/collection-cron /app/deploy/collection-entrypoint.sh

ENTRYPOINT ["/app/hotel-parser"]
