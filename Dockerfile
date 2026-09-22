# The blink server, and only the blink server: it is the one component that
# has to be reachable over public HTTPS, because a blink client cannot fetch
# localhost. The keeper and the CLI stay where they are.

FROM golang:1.25-alpine AS build

WORKDIR /src

# Dependencies first, so a code change does not re-download the module graph.
COPY keeper/go.mod keeper/go.sum ./
RUN go mod download

COPY keeper/ ./

# Static, stripped, and reproducible enough that two builds of the same commit
# agree: -trimpath keeps local paths out of the binary.
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/blink ./cmd/blink

# distroless/static carries CA certificates — needed to reach the RPC node over
# HTTPS — and nothing else. No shell, no package manager, and it runs as a
# non-root user by default.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/blink /blink

EXPOSE 8081
ENTRYPOINT ["/blink"]
