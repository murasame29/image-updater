# Architecture

依存の向きは Infrastructure → Application → Domain の一方向。内側は外側を知らない。

```
cmd/                                    エントリポイント
internal/
  model/                                Domain: エンティティ、ルール、ポート定義（標準ライブラリのみ）
  application/updater/                  Application: ユースケース（model のみに依存）
  infrastructure/
    registry/oci/                       OCI Distribution API の汎用クライアント
    registry/ecr/                       ECR 固有（イベント解釈 + 認証）
    registry/router.go                  レジストリ種別ごとの resolver 振り分け
    queue/sqs/                          SQS からのイベント受信
    githost/github/                     GitHub の clone / commit / push / PR
    manifest/kustomize/                 kustomization.yaml の書き換え
    ruleset/                            設定ファイルの読み込み
  config/                               環境変数の読み込み
  container/                            DI（どの実装をどのポートに差すかの決定）
pkg/lifecycle/                          プロセスのライフサイクル管理
```

## ポート

`internal/model` に定義された interface が、外側との境界になっている。

| ポート               | 責務                                                     |
| -------------------- | -------------------------------------------------------- |
| `EventSource`        | トランスポートからイベントを配送し、ack / 再配信を決める |
| `EventDecoder`       | プロバイダのペイロードを `ImagePushEvent` に変換する     |
| `MetadataResolver`   | push されたイメージの label とサイズを読む               |
| `ManifestRepository` | manifest リポジトリの clone と PR 作成                   |
| `Checkout`           | 作業コピーに対する branch / commit / push                |
| `ManifestPatcher`    | ディレクトリ内の manifest のタグを書き換える             |

ポートは**使う側**（Application 層）の都合で定義してある。実装側が interface を
持たないので、実装を差し替えても Application 層は変わらない。

### イベント受信を 2 段に分けている理由

`EventSource`（トランスポート）と `EventDecoder`（ペイロード解釈）を分けてある。
この 2 つは直交する関心事で、組み合わせが増える。

|                   | ECR              | Artifact Registry | Harbor / GHCR |
| ----------------- | ---------------- | ----------------- | ------------- |
| トランスポート    | SQS              | Pub/Sub           | Webhook       |
| ペイロード        | EventBridge      | Pub/Sub message   | Webhook JSON  |

分けておけば、同じ SQS ソースに別のデコーダを差して他のレジストリのイベントを流せるし、
逆に ECR のイベントを SNS 経由に変えてもデコーダはそのまま使える。

### リトライの判断を Application 層に置いている理由

`EventSource` は「ハンドラがエラーを返したら、それが `model.ErrRetryable` を含むかどうか」
だけを見る。何が一時的な失敗で何が終端かを知っているのはユースケースなので、判断は
`application/updater` に集約してある。

- **終端**（ack する）: ルールに当たらない、タグが拒否されている、manifest が対象イメージを
  参照していない、既に同じタグ、同じ更新の PR が既にある
- **リトライ**（ack しない）: clone / commit / push / PR 作成の失敗、つまり相手側の一時障害

## 新しいレジストリに対応する

メタデータの取得はほぼ OCI Distribution API（`GET /v2/{name}/manifests/{ref}` →
config blob）で、レジストリ間で違うのは**認証だけ**。なので `oci.Client` を再利用して
`oci.Authenticator` を実装するのが基本になる。

### 1. レジストリ種別を足す

```go
// internal/model/image.go
const RegistryHarbor RegistryKind = "harbor"
```

### 2. 認証を実装する

```go
// internal/infrastructure/registry/harbor/authenticator.go

// Authenticator authenticates against Harbor with a robot account.
type Authenticator struct {
    username string
    secret   string
}

func (a *Authenticator) AuthHeader(_ context.Context, _ model.ImageReference) (string, error) {
    credentials := base64.StdEncoding.EncodeToString([]byte(a.username + ":" + a.secret))
    return "Basic " + credentials, nil
}
```

Bearer token を使うレジストリ（GHCR, Artifact Registry）なら `"Bearer " + token` を返す。
`WWW-Authenticate` チャレンジによるトークン交換が必要な場合も、この中に閉じる。

### 3. イベントのデコーダを実装する

```go
// internal/infrastructure/registry/harbor/decoder.go

// Decoder turns a Harbor webhook payload into a domain event.
type Decoder struct{}

func (Decoder) Decode(payload []byte) (model.ImagePushEvent, error) {
    // ... payload をパースする
    // push 以外は model.ErrEventIgnored を返して ack させる
    return model.ImagePushEvent{
        Kind:       model.RegistryHarbor,
        Host:       host,       // harbor.example.com
        Repository: repository, // project/app
        Tag:        tag,
    }, nil
}
```

### 4. コンテナに登録する

```go
// internal/container/container.go
func provideMetadataResolver(awsCfg aws.Config, cfg config.Config) model.MetadataResolver {
    return registry.NewRouter(map[model.RegistryKind]model.MetadataResolver{
        ecradapter.Kind:      oci.NewClient(ecradapter.NewAuthenticator(ecr.NewFromConfig(awsCfg))),
        model.RegistryHarbor: oci.NewClient(harbor.NewAuthenticator(cfg.Harbor.Username, cfg.Harbor.Secret)),
    })
}
```

Application 層と Domain 層には手を入れない。設定ファイルの `registryURI` は先頭セグメントを
レジストリホストとして扱うだけなので、`harbor.example.com/project/$1` のように書ける。

### トランスポートが変わる場合

Webhook で受けるなら `model.EventSource` を実装する。ハンドラが `nil` を返したら ack、
`model.ErrRetryable` を含むエラーなら再配信、それ以外は終端として ack する、という
契約だけ守ればよい。複数のソースを同時に動かす場合は `pkg/lifecycle.Run` に並べて渡す。

## 新しい manifest 形式に対応する

`model.ManifestPatcher` を実装する。kustomize 以外（Helm の `values.yaml` など）も
同じポートの裏に入る。

```go
type ManifestPatcher interface {
    Patch(ctx context.Context, dir string, update model.ImageUpdate) error
}
```

返すエラーの契約。

- `model.ErrManifestNotFound`: 対象ファイルがない
- `model.ErrImageNotManaged`: そのイメージを参照していない
- `model.ErrNoDifference`: 既に同じタグが書かれている

いずれも終端エラーとして扱われ、イベントは ack される。

## テスト

ポートが interface なので、Application 層は fake で完結する。
`internal/application/updater/service_test.go` は fake の
`ManifestRepository` / `Checkout` / `ManifestPatcher` / `MetadataResolver` だけで、
リトライ可否の分類まで含めてユースケースを検証している。

`oci.Client` は `WithScheme("http")` と `WithHTTPClient` で `httptest.Server` に向けられる
ので、レジストリとの通信も実際の HTTP で検証できる。
