# Third-party notices

image-updater itself is licensed under the [Apache License 2.0](LICENSE). This file records the license boundary and audit method for software used to build or run its distributed artifacts. It is informational and does not replace any upstream license text.

## Go application binary

The release binary is statically built with Go 1.27.0 and `CGO_ENABLED=0`. Its Linux `amd64` and `arm64` package graphs are checked with [`google/go-licenses` v2.0.1](https://github.com/google/go-licenses/releases/tag/v2.0.1). The accepted license families are Apache-2.0, MIT, BSD-2-Clause, BSD-3-Clause, and ISC.

Current direct dependencies resolve as follows:

| Dependency | Role | License family |
| --- | --- | --- |
| Go runtime and standard library | compiled language runtime and standard packages | BSD-3-Clause |
| AWS SDK for Go v2 modules | AWS configuration, ECR, SQS | Apache-2.0 |
| `github.com/bradleyfalzon/ghinstallation/v2` | GitHub App authentication | Apache-2.0 |
| `github.com/caarlos0/env/v11` | environment configuration | MIT |
| `github.com/go-git/go-git/v5` | Git operations | Apache-2.0 |
| `github.com/google/go-github/v88` | GitHub API | BSD-3-Clause |
| `go.uber.org/dig` | dependency injection | MIT |
| `go.yaml.in/yaml/v3` | YAML parsing | MIT |
| `golang.org/x/sync` | concurrency primitives | BSD-3-Clause |
| `sigs.k8s.io/kustomize/api` | Kustomize manifest types | Apache-2.0 |
| `github.com/stretchr/testify` | tests only; not linked into the release binary | MIT |

All reachable transitive Go modules are checked by `hack/check_licenses.sh`; `go.sum`-only and test-only modules are not treated as shipped application dependencies. The image also includes the pinned Go runtime and standard-library `LICENSE`, `PATENTS`, and `VERSION` files because those packages are linked into the executable.

### Mixed-license dependency

`github.com/cyphar/filepath-securejoin` v0.7.0 contains both BSD-3-Clause and MPL-2.0 files. The current Linux application graph compiles only `doc.go`, `join.go`, `vfs.go`, and `internal/consts/consts.go`; each file declares `SPDX-License-Identifier: BSD-3-Clause`. The license check fails if a future build starts compiling a differently licensed or embedded file.

The generic license generator is deliberately prevented from copying the entire mixed-license module. After the compiled-file check succeeds, the container build adds one BSD-3-Clause component row and only upstream `LICENSE.BSD`; uncompiled MPL-covered source is not redistributed in the image's notice bundle.

### Distributed license material

Container builds generate a version-specific component report and upstream license/copyright bundle from the actual `CGO_ENABLED=0 ./cmd` package graph. Released images expose these at:

- `/licenses/image-updater/LICENSE`
- `/licenses/image-updater/NOTICE`
- `/licenses/image-updater/THIRD_PARTY_NOTICES.md`
- `/licenses/image-updater/THIRD_PARTY_COMPONENTS.csv`
- `/licenses/third_party/`
- `/licenses/third_party/go.dev/go/{LICENSE,PATENTS,VERSION}`
- `/licenses/third_party/github.com/cyphar/filepath-securejoin/LICENSE.BSD`

The OCI SBOM and provenance attestations remain enabled in addition to these human-readable notices.

## Runtime base image

The final image uses the pinned multi-platform digest `gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab`. Its Debian copyright files remain available under `/usr/share/doc/*/copyright` in the released image. The inspected base contains notices for `base-files`, `ca-certificates` (including Mozilla certificate data), `media-types`, `netbase`, and `tzdata`; their individual GPL-2.0-or-later, GPL-2.0-only, MPL-2.0, public-domain, and other stated terms continue to apply to those base-image files.

## Helm chart and development tooling

The Helm chart has no third-party chart dependencies. Its archive includes its own Apache-2.0 `LICENSE` and `NOTICE` and references the separately distributed application image.

GitHub Actions, Helm, Docker/Buildx, linters, and the Go compiler/toolchain image are build or development tooling and are not copied into distributed artifacts. The Go runtime and standard-library portions linked into the executable are separately identified and distributed with their notices as described above.
