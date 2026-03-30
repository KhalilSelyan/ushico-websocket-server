# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o ushico-websocket-server .

# Runtime stage
FROM alpine:3.21

WORKDIR /app

COPY --from=builder /app/ushico-websocket-server .

EXPOSE 8085

CMD ["./ushico-websocket-server"]
