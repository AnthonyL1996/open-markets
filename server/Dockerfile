# Multi-stage build → a tiny static image (matches BACKEND.md's "Go static binary, tiny image").
FROM golang:1.25-alpine AS build
WORKDIR /src
# No external deps (stdlib only), so just copy and build — no module download step needed.
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/openmarketsd ./cmd/openmarketsd

# Distroless-style runtime: static binary on a minimal base. Runs as non-root.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/openmarketsd /app/openmarketsd
# Persisted JSON state lives here; mount a volume to keep it across restarts.
ENV OM_DATA=/data/openmarkets.json
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/openmarketsd"]
