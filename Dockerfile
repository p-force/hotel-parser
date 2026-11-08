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

ENTRYPOINT ["/app/hotel-parser"]
