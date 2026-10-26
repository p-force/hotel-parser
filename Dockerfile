FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /hotel-parser ./cmd/hotel-parser

FROM alpine:3.22

RUN apk add --no-cache tzdata

ENV HOTEL_PARSER_LOG_FORMAT=json

WORKDIR /app

COPY --from=builder /hotel-parser /app/hotel-parser

ENTRYPOINT ["/app/hotel-parser"]
