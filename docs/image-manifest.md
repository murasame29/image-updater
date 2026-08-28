# well-known image manifest

image-updater が PR を作るとき、kustomization.yaml の `images:` ブロックの直上に
**managed metadata コメントブロック**を書き込む。マニフェストだけを見れば「このイメージの
コードはどのリポジトリのどのコミットにあるのか」が分かる状態にするための仕様。

## 出力例

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
  - ../../base

# --- BEGIN image-updater metadata (auto-generated, do not edit) ---
# schema_version: v1
# generator: image-updater
# images:
#   - image: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/shop/web/api
#     github_repo: https://github.com/example-org/example-service
#     git_sha: a1b2c3d4e5f60718293a4b5c6d7e8f9012345678
#     commit_url: https://github.com/example-org/example-service/commit/a1b2c3d4e5f60718293a4b5c6d7e8f9012345678
#     built_at: "2026-08-10T00:00:00Z"
#     build_run_url: https://github.com/example-org/example-service/actions/runs/123456789
#     extra:
#       cost_center: platform
#       runbook_url: https://example.com/runbook/web-api
#       team: platform
# --- END image-updater metadata ---
images:
  - name: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/shop/web/api
    newTag: alice.a1b2c3d4e5f60718293a4b5c6d7e8f9012345678
```

## スキーマ

`schema_version` は `v1`。ブロックの中身はコメント記号を外すとそのまま YAML として読める。

| キー            | 由来 (image label)                     | 説明                                    |
| --------------- | -------------------------------------- | --------------------------------------- |
| `image`         | ECR イベント                           | タグなしのイメージ URI。`images[].name` と一致 |
| `github_repo`   | `org.opencontainers.image.source`      | **コードが置かれているリポジトリ**      |
| `git_sha`       | `org.opencontainers.image.revision`    | ビルド元のコミット                      |
| `commit_url`    | 導出 (`github_repo` + `git_sha`)       | コミットへのリンク                      |
| `built_at`      | `org.opencontainers.image.created`     | イメージのビルド時刻                    |
| `build_run_url` | `org.opencontainers.image.build.url`   | CI 実行へのリンク                       |
| `extra`         | `org.opencontainers.image.extra.*`     | カスタムラベル。下記参照                |

label が空の項目は出力されない。ECR からラベルを取得できなかった場合は `image` だけの
エントリになる。タグは直下の `images[].newTag` を見れば分かるので manifest には持たせない。

label はイメージをビルドする側の責務。`docker build` の `--label` か Dockerfile の
`LABEL` で付与する。GitHub Actions なら次のように書ける。

```yaml
- name: Build and push
  run: |
    docker build \
      --label "org.opencontainers.image.source=${{ github.server_url }}/${{ github.repository }}" \
      --label "org.opencontainers.image.revision=${{ github.sha }}" \
      --label "org.opencontainers.image.created=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      --label "org.opencontainers.image.build.url=${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}" \
      --label "org.opencontainers.image.build.run_id=${{ github.run_id }}" \
      --label "org.opencontainers.image.build.ref=${{ github.ref_name }}" \
      --label "org.opencontainers.image.build.event=${{ github.event_name }}" \
      --label "org.opencontainers.image.build.actor=${{ github.actor }}" \
      -t "$IMAGE_URI" .
    docker push "$IMAGE_URI"
```

`build.actor` は PR の assignee / reviewer に使われるので、レビューを回したい場合は
付けておく。ラベルが 1 つも無くても更新自体は動く。

## カスタムラベル (`extra`)

`org.opencontainers.image.extra.` prefix を持つラベルは、そのまま `extra` マップとして
出力される。well-known なキーを増やさずに、チームごとの任意のメタデータを載せられる。

```dockerfile
LABEL org.opencontainers.image.extra.team="platform"
LABEL org.opencontainers.image.extra.cost-center="platform"
LABEL org.opencontainers.image.extra.runbook.url="https://example.com/runbook/web-api"
```

キーの正規化ルール。

- prefix を除いた suffix がキーになる
- 小文字化する
- `.` `-` `/` は `_` に置き換える
- それ以外の文字は落とす。落とした結果が空になるキーは無視する
- 前後の `_` は取り除く

値は加工せずそのまま入る。空文字の値と、prefix なしのラベルは無視する。
出力はキーの昇順で安定する。ラベルが 1 つもない場合 `extra` キー自体を出力しない。

`extra` は well-known キーとは別のマップなので、`git_sha` などと衝突しない。

## エントリの同一性

エントリは `image` で同一と判定し、一致するエントリだけを置き換える。他のイメージの
エントリはそのまま残る。出力は `image` の昇順で安定する。

**制約**: `images:` は同じ `name` を複数持てる（`alice.<sha>` / `bob.<sha>` のように
タグ prefix でスロットを分ける運用）。manifest は `image` だけをキーにしているため、
この構成では 1 エントリに集約され、直近に更新したスロットの情報しか残らない。
スロットごとに持たせたい場合は `tag` フィールドを復活させてキーに含める必要がある。

## ファイル書き換えの挙動

kustomization.yaml は行単位で編集する。書き換わるのは次の行だけ。

- 更新対象の `newTag:` の値
- managed metadata ブロック

そのため以下は保持される。

- 手書きのコメント（ブロック前後、インラインコメントを含む）
- 空行、キーの並び順、インデントスタイル
- `types.Kustomization` が知らないフィールド

managed metadata ブロックは `images:` 直上の連続コメント領域の中を探し、見つかれば
その場で差し替える。見つからなければ同領域の先頭に挿入するので、`images:` に隣接した
手書きコメントは `images:` のすぐ上に残る。

ブロックの中身が壊れて YAML として読めない場合は警告ログを出して新規生成し直す。

## 無効化

registry config ごとに `imageManifest: false` で書き込みを止められる。未指定は有効。

```yaml
- githubRepository: https://github.com/example-org/example-manifests/services/$1/$2/$3/overlays/staging
  registryURI: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/$1/$2/$3
  env: staging
  imageManifest: false
```
