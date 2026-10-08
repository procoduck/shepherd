FROM golang:1.27-alpine@sha256:738d1cf061836894ff6bb8c33881080ac66de8cf0586615012a0c8f592649cfa AS builder
WORKDIR /app
COPY main.go .
RUN go mod init mockmsft && CGO_ENABLED=0 GOOS=linux go build -o /mockmsft .

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=builder /mockmsft /mockmsft
ENTRYPOINT ["/mockmsft"]
