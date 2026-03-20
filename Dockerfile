# Stage 1: Build
FROM cgr.dev/chainguard/go:latest AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /herald ./cmd/herald

# Stage 2: Runtime
FROM cgr.dev/chainguard/static:latest

COPY --from=builder /herald /herald

ENTRYPOINT ["/herald"]
