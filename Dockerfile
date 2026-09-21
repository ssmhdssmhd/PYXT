FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o pyxt .

FROM alpine:3.20

WORKDIR /app
COPY --from=builder /app/pyxt .
COPY --from=builder /app/api_docs.html .
COPY --from=builder /app/player.html .
COPY --from=builder /app/video_player.html .
COPY --from=builder /app/jiexiyuan.txt .

ENV PORT=5000
EXPOSE 5000

ENTRYPOINT ["/app/pyxt"]
