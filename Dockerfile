# syntax=docker/dockerfile:1

# --- Build stage ---
FROM golang:1.25-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG NUMBER=dev
ARG COMMIT=dev
ARG COMMIT_TIME=unknown
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X github.com/racso/poof/version.Number=${NUMBER} -X github.com/racso/poof/version.Commit=${COMMIT} -X github.com/racso/poof/version.CommitTime=${COMMIT_TIME}" -o poof .

# --- Runtime stage ---
# docker:cli gives us the Docker CLI so Poof! can exec docker commands.
# ca-certificates is included so HTTPS calls to GitHub API and ACME work.
FROM docker:27-cli
COPY --from=builder /build/poof /usr/local/bin/poof
ENTRYPOINT ["poof"]
CMD ["server"]
