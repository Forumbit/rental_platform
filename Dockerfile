# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN for cmd in api migrate seed-user; do \
      CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/$cmd ./cmd/$cmd || exit 1; \
    done

# Static binaries only need CA certificates (for SMTP and S3 over TLS), which
# distroless/static provides. The image runs as an unprivileged user.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/ /app/
COPY config/config.yaml /app/config/config.yaml
EXPOSE 8080
ENTRYPOINT ["/app/api", "-config", "/app/config/config.yaml"]
