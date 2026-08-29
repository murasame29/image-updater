# Configuration

日本語: [設定](../ja/configuration.md)

The bundled runtime receives Amazon ECR image push events through EventBridge and SQS, then updates
`kustomization.yaml` files hosted on GitHub. Environment variables configure the event transport and process,
while `config.yaml` maps images to manifests and configures pull request messages.

## Configuration sources

- Environment variables: process, AWS, GitHub App, and SQS settings
- `config.yaml`: message templates and rules mapping registry images to GitOps manifests

Examples:

- [`.env.example`](../../example/.env.example)
- [`config.example.yaml`](../../example/config.example.yaml)

## Prerequisites

- An SQS queue receiving ECR push events through EventBridge
- A GitHub App that can push to the manifest repository
  - `contents: write`
  - `pull_requests: write`
- Registry read permissions only when label annotations are enabled
  - `ecr:GetAuthorizationToken`
  - `ecr:BatchGetImage`
  - `ecr:GetDownloadUrlForLayer`

## Environment variables

| Environment variable | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `CONFIG_PATH` | path | Yes | - | Path to `config.yaml` |
| `AWS_QUEUE_URI` | string | Yes | - | URL of the SQS queue receiving push events |
| `GITHUB_APPLICATION_ID` | int64 | Yes | - | GitHub App ID |
| `GITHUB_INSTALLATION_ID` | int64 | Yes | - | GitHub App installation ID |
| `GITHUB_USERNAME` | string | Yes | - | Git username used for commits and pushes |
| `GITHUB_CRT_PATH` | path | Yes | - | Path to the GitHub App private key in PEM format |
| `GITHUB_AUTHOR_EMAIL` | string | No | `{GITHUB_USERNAME}@users.noreply.github.com` | Commit author email |
| `GITHUB_BASE_BRANCH` | string | No | `main` | Pull request base branch |
| `IMAGE_LABEL_ANNOTATION_ENABLED` | bool | No | `false` | Enables [label annotations](#label-annotations) |
| `IMAGE_MANIFEST_INDENT` | int (2-9) | No | `2` | YAML indentation inside the metadata comment block |
| `LOG_LEVEL` | `debug` / `info` / `warn` / `error` | No | `info` | Log level |
| `INTERVAL` | duration | No | `10s` | Extra delay between SQS receive loops. This is not a registry tag polling interval. Use `0s` for minimum latency |
| `CONCURRENCY` | int (1-10) | No | `10` | Maximum number of events handled concurrently |
| `SHUTDOWN_TIMEOUT` | duration | No | `30s` | Maximum shutdown wait for in-flight events |
| `WORK_DIR` | path | No | OS temporary directory | Repository clone directory |
| `AWS_QUEUE_WAIT_TIME` | int (1-20) | No | `20` | SQS long polling duration in seconds |
| `AWS_QUEUE_VISIBILITY_TIMEOUT` | int (1-43200) | No | `20` | Initial visibility lease in seconds; renewed at half the granted interval and capped by SQS's absolute 12-hour lifetime |
| `AWS_QUEUE_MAX_MESSAGES` | int (1-10) | No | `10` | Receive batch limit, capped to `CONCURRENCY` at runtime |

The process rejects missing required values and out-of-range values at startup. AWS credentials follow the
standard AWS SDK provider chain, including environment variables, IRSA, and instance profiles.

## `config.yaml`

The recommended document shape is a top-level mapping containing `messages` and `rules`. Omitted message fields
use English defaults. The legacy top-level rule sequence remains readable, but message customization requires the
mapping form.

| Field | Required | Description |
| --- | --- | --- |
| `messages` | No | Go templates for the pull request title, pull request body, and commit message |
| `rules` | Yes | Rules mapping registry repositories to manifest locations |

### Rule schema

| Field | Required | Description |
| --- | --- | --- |
| `registryURI` | Yes | Image pattern. The first segment is the registry host. `$n` captures a segment and `*` is a wildcard |
| `githubRepository` | Yes | Manifest path in `https://github.com/{owner}/{repo}/...` form. Captures can be expanded and one path segment may be `**` |
| `environment` | Yes | Environment name used in branch names, commit messages, and pull request titles |
| `allowImageTag` | No | Allowed tag. Prefix with `regexp:` for a regular expression. Omission allows every tag |
| `denyImageTag` | No | Denied tag list. `regexp:` is supported and denial takes precedence over allowance |
| `imageManifest` | No | Whether this rule writes the well-known image manifest. Defaults to `true` and is ignored when process-wide label annotations are disabled |

`environment` is required. The former `env` key is not accepted and causes startup validation to fail.

### Captures and wildcards

- `$1`, `$2`, `$3`, ...: capture `registryURI` path segments and reuse them in `githubRepository`
- `*` in `registryURI`: match one segment without capturing it
- `**` in `githubRepository`: match zero or more directory segments; at most one whole path segment may use it
- `:tagPattern` at the end of `registryURI`: split a tag on `.` and capture tag pieces

### Example

```yaml
messages:
  pullRequestTitle: '[{{.Environment}}][Image Updater][{{.ImageName}}] Update image'
  pullRequestBody: |-
    ## Image update

    | Item | Value |
    | --- | --- |
    | Environment | {{.Environment}} |
    | Image | {{.Image}} |
    | Tag | {{.ImageTag}} |
  commitMessage: '[{{.Environment}}][image-committer][{{.ImageRepository}}] Update image'

rules:
  # push: .../apps/shop/web/api:abc1234
  #   -> every kustomization.yaml below services/shop/web/api/overlays
  - registryURI: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/$1/$2/$3
    githubRepository: https://github.com/example-org/example-manifests/services/$1/$2/$3/overlays/**
    allowImageTag: regexp:^[0-9a-f]{7,40}$
    denyImageTag: [latest, main]
    environment: staging

  # Multiple paths referencing one image are combined into one pull request for the same GitHub repository.
  - registryURI: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/shared-api
    githubRepository: https://github.com/example-org/example-manifests/services/shop/api/overlays/production
    environment: production
  - registryURI: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/shared-api
    githubRepository: https://github.com/example-org/example-manifests/services/admin/api/overlays/production
    environment: production
```

### Rule matching

1. Compare the first `registryURI` segment with the registry host using an exact match.
2. Require equal repository path segment counts.
3. Match each segment as a literal, `$n`, or `*`.
4. Keep only matching rules with the highest number of literal matches.
5. Apply every rule tied for that highest score in declaration order.
6. Filter each remaining rule independently with `allowImageTag` and `denyImageTag`. Rejection by one rule does not reject another tied rule.

For `apps/tenants/example/app`, the second rule below wins because it has three literal matches. This lets a
specific rule override a generic fallback.

```yaml
- registryURI: .../apps/$1/$2/$3         # one literal: apps
- registryURI: .../apps/tenants/*/app    # three literals: apps, tenants, app
```

### Recursive `githubRepository` wildcard

`**` matches zero or more directory segments. Every matching directory containing a regular
`kustomization.yaml` file becomes an update target. For example, `services/$1/overlays/**` includes the
`overlays` directory and all of its descendants.

After cloning, traversal starts at the fixed prefix before the wildcard. It does not follow `.git` or symlinked
directories, and it rejects symlinked or non-regular `kustomization.yaml` files. Results are sorted and deduplicated
as repository-relative paths. No matches produce `ErrManifestNotFound`.

If any selected manifest does not manage the target image, the updater does not commit a partial repository update.
Constrain `**` to the set of manifests expected to manage the image.

### Multiple path aggregation

Rules tied for the highest score and allowing the tag are grouped by GitHub owner and repository using a
case-insensitive comparison. Paths in one repository are sorted and applied to one checkout, producing one commit,
one push, and one pull request. Different repositories necessarily produce separate pull requests.

Duplicate paths with identical `environment` and `imageManifest` values are updated once. Exact configured-path
conflicts fail before remote I/O; conflicts revealed only after `**` expansion fail after checkout but before branch
creation, commit, or push. A path already using the tag is skipped, and a pull request is created when at least one
path in the repository changes.

## Branch names and message templates

Branch names are idempotency keys for redelivery and cannot be configured.

```text
image_updater_{imageName}_{environment}_{imageTag}_{identityHash}
```

- `{imageName}`: repository name without its first namespace; `/` becomes `_`
- `{environment}`: the environment when every target agrees, otherwise `multi`
- `{imageTag}`: tag from the push event
- `{identityHash}`: first 16 hexadecimal characters of a SHA-256 over the registry host, full image repository, tag, manifest repository, and sorted target patterns and environments

The hash uses pre-expansion `**` patterns, so changes to matching directories on the base branch do not change the
branch identity. For multiple environments, commit and pull request templates receive sorted, comma-separated names.

| Repository | Environment | Tag | Example branch |
| --- | --- | --- | --- |
| `apps/shop/web/api` | `staging` | `abc1234` | `image_updater_shop_web_api_staging_abc1234_<identityHash>` |
| `apps/platform/image-updater` | `development` | `def5678` | `image_updater_platform_image-updater_development_def5678_<identityHash>` |

Omitted message fields use these English templates:

```yaml
messages:
  pullRequestTitle: '[{{.Environment}}][Image Updater][{{.ImageName}}] Update image'
  pullRequestBody: '{{.DefaultBody}}'
  commitMessage: '[{{.Environment}}][image-committer][{{.ImageRepository}}] Update image'
```

| Variable | Description |
| --- | --- |
| `{{.Environment}}` | Sorted and joined environment names, such as `development,staging` |
| `{{.ImageName}}` | Image name without the first namespace |
| `{{.ImageRepository}}` | Full image repository name |
| `{{.Image}}` | Image name including the registry host, without a tag |
| `{{.ImageTag}}` | Pushed tag |
| `{{.DefaultBody}}` | Default English pull request body generated from image labels |

A custom body may reuse `DefaultBody` or replace it completely. Other body values are escaped for normal Markdown
text. Templates support direct field substitution only; `if`, `with`, `range`, function calls, variables, and template
invocations are rejected. Templates are parsed and validated once at startup. Pull request titles are limited to 256
characters, commit messages to 4,096 characters, and pull request bodies to 65,536 characters. Titles and commit
messages cannot contain newlines or control characters. Use a YAML block scalar (`|-`) for multiline bodies.

## Label annotations

Label annotation is an optional feature that enriches pull requests with OCI labels embedded in the image. It is
disabled by default and enabled with `IMAGE_LABEL_ANNOTATION_ENABLED=true`.

When enabled, the updater:

- adds commit, branch, author, and CI run links to the pull request body;
- assigns and requests review from `org.opencontainers.image.build.actor`; and
- writes the well-known image manifest metadata comment before `images:`.

When disabled, the updater does not query registry metadata, so registry read permissions and build pipeline labels
are unnecessary. Enabling it requires both labels from the build and registry read access. Missing labels do not stop
the tag update; only available context is included.

### Labels are untrusted input

A principal allowed to push an image may have less privilege than someone allowed to approve a manifest pull request.
Rendering labels directly could inject Markdown, mentions, or HTML comments, so values are normalized at the domain
boundary.

| Field | Handling |
| --- | --- |
| `pr.title` | Replace newlines and control characters with spaces, truncate to 512 characters, and escape for Markdown rendering |
| `image.source`, `build.url` | Accept only absolute HTTP(S) URLs without credentials |
| `pr.author`, `build.actor` | Accept only GitHub handle syntax |
| `pr.number` | Accept only decimal digits |
| `image.revision`, `image.created`, `build.ref`, `build.event`, `build.run_id` | Accept only `[A-Za-z0-9._\-/:+]` |
| `extra.*` | Apply the same normalization as `pr.title` |

Values that cannot be normalized are dropped rather than repaired. Labels are optional context, so invalid values do
not stop image tag updates. See the well-known image manifest for the label schema and generated
comment block.

## Related references

- [`config.example.yaml`](../../example/config.example.yaml)
