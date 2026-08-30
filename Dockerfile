ARG GO_VERSION=1.27.0
ARG DEBIAN_VERSION=bookworm

# build
FROM golang:${GO_VERSION}-${DEBIAN_VERSION} AS build
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
SHELL ["/bin/bash", "-eo", "pipefail", "-c"]
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -buildvcs=false \
    -o /build/main \
    -ldflags="-w -s \
      -X github.com/murasame29/image-updater/internal/version.Version=${VERSION} \
      -X github.com/murasame29/image-updater/internal/version.Commit=${COMMIT} \
      -X github.com/murasame29/image-updater/internal/version.BuildDate=${BUILD_DATE}" \
    ./cmd

# prod
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build --chmod=755 /build/main /app/main
USER 1000
ENTRYPOINT ["/app/main"]
