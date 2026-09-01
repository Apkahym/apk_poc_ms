FROM golang:1.22-alpine AS builder

WORKDIR /src

COPY go.mod ./
COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/apk_poc_ms .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates
WORKDIR /app

COPY --from=builder /out/apk_poc_ms /app/apk_poc_ms

EXPOSE 8080
ENV PORT=8080

CMD ["/app/apk_poc_ms"]
