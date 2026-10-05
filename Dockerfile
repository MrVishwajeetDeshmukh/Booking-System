FROM golang:1.25.0-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o server .

FROM alpine:latest
WORKDIR /app
RUN addgroup -S app && adduser -S app -G app
COPY --from=builder --chown=app:app /app/server ./server
USER app
ENV PORT=8080
EXPOSE 8080
CMD ["./server"]
