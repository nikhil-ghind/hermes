# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.22-alpine AS build
WORKDIR /src

# Cache module downloads separately from source builds.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static, stripped binary for a minimal runtime image.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/hermes ./cmd/hermes

# ---- runtime stage ----
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

COPY --from=build /out/hermes /app/hermes

# Non-root by default (distroless nonroot user).
USER nonroot:nonroot

EXPOSE 8080
ENTRYPOINT ["/app/hermes"]
