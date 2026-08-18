FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY main.go .
RUN go mod init mockmsft && CGO_ENABLED=0 GOOS=linux go build -o /mockmsft .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /mockmsft /mockmsft
ENTRYPOINT ["/mockmsft"]
