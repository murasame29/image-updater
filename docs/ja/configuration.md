# 設定

English: [Configuration](../en/configuration.md)

現在同梱しているruntimeは、ECRのimage push eventをEventBridgeとSQS経由で受信し、
GitHub上の`kustomization.yaml`を更新する。event transportは環境変数、imageとmanifestの対応や
PR messageは`config.yaml`で設定する。

## 設定ソース

- 環境変数: process、AWS、GitHub App、SQSの設定
- `config.yaml`: message templateと、registry imageからGitOps manifestへの対応rule

設定例:

- [`.env.example`](../../example/.env.example)
- [`config.example.yaml`](../../example/config.example.yaml)

## 前提

- ECRのpush eventがEventBridge経由で届くSQS queue
- manifest repositoryへpushできるGitHub App
  - `contents: write`
  - `pull_requests: write`
- label注釈を有効にする場合のみregistry read権限
  - `ecr:GetAuthorizationToken`
  - `ecr:BatchGetImage`
  - `ecr:GetDownloadUrlForLayer`

## 環境変数

| 環境変数 | 型 | 必須 | 既定値 | 説明 |
| --- | --- | --- | --- | --- |
| `CONFIG_PATH` | path | Yes | - | `config.yaml`のpath |
| `AWS_QUEUE_URI` | string | Yes | - | push eventが届くSQS queue URL |
| `GITHUB_APPLICATION_ID` | int64 | Yes | - | GitHub App ID |
| `GITHUB_INSTALLATION_ID` | int64 | Yes | - | GitHub App installation ID |
| `GITHUB_USERNAME` | string | Yes | - | commitとpushに使うGit username |
| `GITHUB_CRT_PATH` | path | Yes | - | GitHub App private key（PEM）のpath |
| `GITHUB_AUTHOR_EMAIL` | string | No | `{GITHUB_USERNAME}@users.noreply.github.com` | commit author email |
| `GITHUB_BASE_BRANCH` | string | No | `main` | PRのbase branch |
| `IMAGE_LABEL_ANNOTATION_ENABLED` | bool | No | `false` | [labelによる注釈](#labelによる注釈)を有効にする |
| `IMAGE_MANIFEST_INDENT` | int（2-9） | No | `2` | metadata comment block内のYAML indent幅 |
| `LOG_LEVEL` | `debug` / `info` / `warn` / `error` | No | `info` | log level |
| `INTERVAL` | duration | No | `10s` | SQS受信loop間の追加待機時間。registry tag polling間隔ではない。最小遅延なら`0s` |
| `CONCURRENCY` | int（1-10） | No | `10` | 同時に処理するevent数 |
| `SHUTDOWN_TIMEOUT` | duration | No | `30s` | shutdown時に処理中eventの完了を待つ上限 |
| `WORK_DIR` | path | No | OSのtemporary directory | repository clone先 |
| `AWS_QUEUE_WAIT_TIME` | int（1-20） | No | `20` | SQS long polling時間（秒） |
| `AWS_QUEUE_VISIBILITY_TIMEOUT` | int（1-43200） | No | `20` | 初期visibility lease（秒）。実際に延長できた期間の半周期で更新し、SQSの受信から12時間という絶対上限を超えないようcapする |
| `AWS_QUEUE_MAX_MESSAGES` | int（1-10） | No | `10` | 1回のreceive上限。実際は`CONCURRENCY`以下へ調整する |

必須値の欠落や範囲外の値は起動時にrejectする。AWS credentialはSDKの標準解決順
（環境変数、IRSA、instance profileなど）に従う。

## `config.yaml`

推奨形式は、`messages`と`rules`を持つtop-level mapping。`messages`を省略すると英語の既定値を使う。
旧形式のtop-level rule sequenceも読み込めるが、messageを設定する場合はmapping形式を使う。

| 項目 | 必須 | 説明 |
| --- | --- | --- |
| `messages` | No | PR title、PR body、commit messageのGo template |
| `rules` | Yes | registry repositoryとmanifestの場所を対応づけるrule list |

### Rule schema

| 項目 | 必須 | 説明 |
| --- | --- | --- |
| `registryURI` | Yes | 対象image pattern。先頭segmentはregistry host。`$n`でcapture、`*`でwildcard |
| `githubRepository` | Yes | `https://github.com/{owner}/{repo}/...`形式のmanifest path。`$n`を展開でき、pathの1 segmentへ`**`を指定できる |
| `environment` | Yes | branch名、commit message、PR titleに使うenvironment名 |
| `allowImageTag` | No | 許可するtag。`regexp:` prefixでregular expression。未指定はすべて許可 |
| `denyImageTag` | No | 拒否するtag list。`regexp:`も利用でき、`allowImageTag`より優先 |
| `imageManifest` | No | このruleでwell-known image manifestを書くか。既定は`true`。process全体のlabel注釈が無効なら無視する |

`environment`は必須。旧keyの`env`は互換処理せず、起動時にrejectする。

### 変数とwildcard

- `$1`, `$2`, `$3` ...: `registryURI`のpath segmentをcaptureし、`githubRepository`で再利用する
- `registryURI`の`*`: 任意の1 segmentにmatchするがcaptureしない
- `githubRepository`の`**`: 0個以上のdirectory segmentにmatchする。path内で最大1個、segment全体として指定する
- `registryURI`末尾の`:tagPattern`: tagを`.`で分割したpieceもcaptureできる

### 設定例

```yaml
messages:
  pullRequestTitle: '[{{.Environment}}][Image Updater][{{.ImageName}}] イメージの更新'
  pullRequestBody: |-
    ## イメージの更新

    | 項目 | 値 |
    | --- | --- |
    | 環境 | {{.Environment}} |
    | イメージ | {{.Image}} |
    | タグ | {{.ImageTag}} |
  commitMessage: '[{{.Environment}}][image-committer][{{.ImageRepository}}] イメージの更新'

rules:
  # push: .../apps/shop/web/api:abc1234
  #   -> services/shop/web/api/overlays配下の全kustomization.yaml
  - registryURI: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/$1/$2/$3
    githubRepository: https://github.com/example-org/example-manifests/services/$1/$2/$3/overlays/**
    allowImageTag: regexp:^[0-9a-f]{7,40}$
    denyImageTag: [latest, main]
    environment: staging

  # 同じimageを参照する複数path。同じGitHub repositoryなので1 PRにまとめる
  - registryURI: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/shared-api
    githubRepository: https://github.com/example-org/example-manifests/services/shop/api/overlays/production
    environment: production
  - registryURI: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/shared-api
    githubRepository: https://github.com/example-org/example-manifests/services/admin/api/overlays/production
    environment: production
```

### Matching rule

1. `registryURI`先頭segmentのregistry hostを完全一致で比較する
2. repository pathのsegment数を比較する
3. 各segmentをliteral、`$n`、`*`として照合する
4. matchしたruleのうち、literal一致数が最大のruleだけを残す
5. 最大数で同率のruleは宣言順にすべて適用する
6. 各ruleを`allowImageTag`と`denyImageTag`で個別にfilterする。1 ruleの拒否は同率の別ruleへ影響しない

たとえば次の2 ruleが`apps/tenants/example/app`にmatchした場合、literal一致数が3の後者だけを選ぶ。
具体的なruleが汎用fallbackを上書きする意味を維持するためのpriorityである。

```yaml
- registryURI: .../apps/$1/$2/$3         # literal一致: 1（apps）
- registryURI: .../apps/tenants/*/app    # literal一致: 3（apps, tenants, app）
```

### `githubRepository`のrecursive wildcard

`**`は0個以上のdirectory segmentにmatchし、配下で通常fileの`kustomization.yaml`を持つdirectoryを
すべて更新対象へ展開する。たとえば`services/$1/overlays/**`は`overlays`直下と全subdirectoryを対象にする。

repositoryをcloneした後、wildcardより前の固定prefixだけを起点に走査する。`.git`とsymlink directoryは
辿らず、symlinkまたは通常fileではない`kustomization.yaml`も受理しない。展開結果はrepository相対pathで
sort・dedupeする。matchが0件なら`ErrManifestNotFound`で終了する。

選ばれたmanifestのいずれかが対象imageを管理していない場合は、repository内の部分更新をcommitしない。
`**`は対象imageを管理するmanifestの範囲へ絞る。

### 複数pathの集約

同率で選ばれ、tagを許可されたruleはGitHub owner/repository単位でgroup化する。owner/repositoryの比較は
大文字・小文字を区別しない。同じrepository内のpathはsortして1 checkoutへ適用し、1 commit・1 push・
1 PRにまとめる。別repositoryはそれぞれ別PRになる。

同じpathと同じ`environment` / `imageManifest`の重複は1回だけ更新する。設定時点で同一pathと分かる競合は
remote I/O前にrejectし、`**`展開後に判明する競合はcheckout後、branch作成・commit・push前にrejectする。
path単位の「既に同じtag」はskipし、同じrepositoryで1 path以上に差分があればPRを作る。

## Branch名とmessage template

branch名は再配信時のidempotency keyになるため設定では変更できない。

```text
image_updater_{imageName}_{environment}_{imageTag}_{identityHash}
```

- `{imageName}`: repository名から先頭namespaceを除いた部分。`/`は`_`へ変換する
- `{environment}`: 全targetが同じならその値。複数environmentなら`multi`
- `{imageTag}`: push eventのtag
- `{identityHash}`: registry host、完全なimage repository、tag、manifest repository、sort済みtarget patternとenvironmentから作るSHA-256の先頭16桁

`**`の展開結果がbase branch上で変化しても、展開前patternを使うためbranch identityは変わらない。
複数environmentのcommit messageとPR titleには、sortしてcommaで結合した値を渡す。

| Repository | environment | tag | Branch例 |
| --- | --- | --- | --- |
| `apps/shop/web/api` | `staging` | `abc1234` | `image_updater_shop_web_api_staging_abc1234_<identityHash>` |
| `apps/platform/image-updater` | `development` | `def5678` | `image_updater_platform_image-updater_development_def5678_<identityHash>` |

`messages`の各項目を省略した場合は次の英語templateを使う。

```yaml
messages:
  pullRequestTitle: '[{{.Environment}}][Image Updater][{{.ImageName}}] Update image'
  pullRequestBody: '{{.DefaultBody}}'
  commitMessage: '[{{.Environment}}][image-committer][{{.ImageRepository}}] Update image'
```

| 変数 | 内容 |
| --- | --- |
| `{{.Environment}}` | sort・結合済みのenvironment名（例: `development,staging`） |
| `{{.ImageName}}` | 先頭namespaceを除いたimage名 |
| `{{.ImageRepository}}` | 完全なimage repository名 |
| `{{.Image}}` | registry hostを含むimage名（tagなし） |
| `{{.ImageTag}}` | pushされたtag |
| `{{.DefaultBody}}` | labelから生成した既定の英語PR body |

`DefaultBody`はそのまま利用しても、custom bodyで完全に置き換えてもよい。bodyへ渡す他の値はMarkdownの
通常text向けにescape済み。templateはfieldの直接展開だけを許可し、`if`、`with`、`range`、function call、
variable、template invocationはrejectする。起動時に1回parse・validateし、PR titleは256文字、commit
messageは4,096文字、PR bodyは65,536文字を上限とする。titleとcommit messageには改行やcontrol characterを
許可しない。複数行bodyにはYAML block scalar（`|-`）を使う。

## Labelによる注釈

imageに埋め込まれたOCI labelからPRへcontextを追加するoptional機能。既定では無効で、
`IMAGE_LABEL_ANNOTATION_ENABLED=true`にすると有効になる。

有効時の動作:

- PR bodyへcommit、branch、author、CI runへのlinkを追加する
- `org.opencontainers.image.build.actor`をassigneeとreviewerへ設定する
- `images:`直前へwell-known image manifestのmetadata commentを書く

無効時はregistryへmetadataを問い合わせず、registry read権限やbuild pipelineでのlabel付与も不要。
有効にする場合はbuild側のlabelとregistry read権限が必要になる。labelが欠けていてもtag更新は継続し、
取得できたcontextだけをPRへ反映する。

### Labelは信頼されない入力として扱う

registryへpushできる主体は、manifest PRをapproveできる主体より低い権限である可能性がある。
labelをそのままPRへ描画するとMarkdown、mention、HTML commentを注入できるため、domainへ入る時点で正規化する。

| Field | 扱い |
| --- | --- |
| `pr.title` | 改行とcontrol characterをspaceへ変換し、512文字で切る。Markdown描画時にescapeする |
| `image.source`, `build.url` | credentialを含まない絶対HTTP(S) URLだけを受理する |
| `pr.author`, `build.actor` | GitHub handle形式だけを受理する |
| `pr.number` | 10進数だけを受理する |
| `image.revision`, `image.created`, `build.ref`, `build.event`, `build.run_id` | `[A-Za-z0-9._\-/:+]`だけを受理する |
| `extra.*` | `pr.title`と同じ正規化を適用する |

正規化できない値は修復せずdropする。labelは補助contextであり、不正値がtag更新を止めることはない。
label schemaと生成されるcomment blockの詳細はwell-known image manifestを参照。

## 関連資料

- [`config.example.yaml`](../../example/config.example.yaml)
