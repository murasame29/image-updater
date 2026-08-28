ARG GO_VERSION=1.27.0
ARG DEBIAN_VERSION=bookworm

# build
FROM golang:${GO_VERSION}-${DEBIAN_VERSION} AS build
SHELL ["/bin/bash", "-eo", "pipefail", "-c"]
WORKDIR /build
COPY . .
RUN CGO_ENABLED=0 go build -o /build/main -ldflags="-w -s" cmd/main.go

# prod
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build --chmod=755 /build/main /app/main
USER 1000
CMD ["/app/main"]
