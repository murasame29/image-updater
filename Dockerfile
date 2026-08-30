ARG GO_VERSION=1.27.0
ARG DEBIAN_VERSION=bookworm
ARG GO_LICENSES_VERSION=v2.0.1

# build
FROM golang:${GO_VERSION}-${DEBIAN_VERSION}@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452 AS build
ARG GO_LICENSES_VERSION
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
    ./cmd && \
    securejoin_module='github.com/cyphar/filepath-securejoin' && \
    securejoin_dir="$(go list -m -f '{{.Dir}}' "${securejoin_module}")" && \
    securejoin_version="$(go list -m -f '{{.Version}}' "${securejoin_module}")" && \
    go_root="$(go env GOROOT)" && \
    go_version="$(go env GOVERSION)" && \
    mkdir -p /build/licenses/image-updater && \
    CGO_ENABLED=0 go run "github.com/google/go-licenses/v2@${GO_LICENSES_VERSION}" report \
      --ignore github.com/murasame29/image-updater \
      --ignore "${securejoin_module}" \
      ./cmd \
      > /build/licenses/image-updater/THIRD_PARTY_COMPONENTS.csv && \
    if grep --fixed-strings --quiet "${securejoin_module}" \
      /build/licenses/image-updater/THIRD_PARTY_COMPONENTS.csv; then \
      echo 'filepath-securejoin was not excluded from the generated report' >&2; \
      exit 1; \
    fi && \
    printf '%s,%s,%s\n' \
      "${securejoin_module}@${securejoin_version}" \
      "https://github.com/cyphar/filepath-securejoin/blob/${securejoin_version}/LICENSE.BSD" \
      'BSD-3-Clause' \
      >> /build/licenses/image-updater/THIRD_PARTY_COMPONENTS.csv && \
    printf '%s,%s,%s\n' \
      "go.dev/stdlib@${go_version}" \
      'https://go.dev/LICENSE' \
      'BSD-3-Clause' \
      >> /build/licenses/image-updater/THIRD_PARTY_COMPONENTS.csv && \
    CGO_ENABLED=0 go run "github.com/google/go-licenses/v2@${GO_LICENSES_VERSION}" save \
      --ignore github.com/murasame29/image-updater \
      --ignore "${securejoin_module}" \
      --save_path=/build/licenses/third_party \
      ./cmd && \
    mkdir -p \
      /build/licenses/third_party/github.com/cyphar/filepath-securejoin \
      /build/licenses/third_party/go.dev/go && \
    cp "${securejoin_dir}/LICENSE.BSD" \
      /build/licenses/third_party/github.com/cyphar/filepath-securejoin/ && \
    cp "${go_root}/LICENSE" "${go_root}/PATENTS" "${go_root}/VERSION" \
      /build/licenses/third_party/go.dev/go/ && \
    securejoin_notices=(/build/licenses/third_party/github.com/cyphar/filepath-securejoin/*) && \
    [[ ${#securejoin_notices[@]} -eq 1 ]] && \
    [[ "${securejoin_notices[0]##*/}" == 'LICENSE.BSD' ]] && \
    cp LICENSE NOTICE THIRD_PARTY_NOTICES.md /build/licenses/image-updater/

# prod
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
WORKDIR /app
COPY --from=build --chmod=755 /build/main /app/main
COPY --from=build /build/licenses /licenses
USER 1000
ENTRYPOINT ["/app/main"]
