# kube-readiness

[![ci](https://github.com/OTA-EITA/kube-readiness/actions/workflows/ci.yml/badge.svg)](https://github.com/OTA-EITA/kube-readiness/actions/workflows/ci.yml)

Kubernetes のワークロードを**本番運用のベストプラクティス**（信頼性・効率・セキュリティ）で採点する CLI です。
マニフェスト（ファイル、ディレクトリ、`helm template` の出力）と、稼働中のクラスタの両方に使えます。

A CLI that scores Kubernetes workloads for production readiness — reliability, efficiency and security —
from manifests or a live cluster, in the spirit of Polaris.

## 特徴

- **デプロイ前（CI）と稼働中（クラスタ）の両方**: 同じチェックで、PR のマニフェストも本番クラスタも採点できます
- **アップデートを止める PDB を検出**: `minAvailable` がレプリカ数と同じ、`maxUnavailable: 0` など、ノードのドレインを永遠に待たせる PDB を danger として扱います（[kube-upgrade-preflight](https://github.com/OTA-EITA/kube-upgrade-preflight) と同じ観点を、デプロイ前に止めます）
- **SARIF 出力でマニフェストの行を指す**: GitHub の Code scanning に取り込むと、PR の該当マニフェストに注釈が付きます
- **スコアとグレード**: ワークロードごと、カテゴリごと、全体の点数（A〜F）を出し、`--min-score` で CI のゲートにできます

## チェック

| ID | カテゴリ | 重大度 | 内容 |
|---|---|---|---|
| REL-001 | reliability | warning | readinessProbe がある |
| REL-002 | reliability | warning | livenessProbe がある |
| REL-003 | reliability | warning | レプリカが 2 以上 |
| REL-004 | reliability | warning | PodDisruptionBudget で守られている |
| REL-005 | reliability | **danger** | PDB が少なくとも 1 台の停止を許す（ドレインが止まらない） |
| REL-006 | reliability | warning | topologySpreadConstraints か podAntiAffinity でノードに分散している |
| REL-007 | reliability | **danger** | イメージがタグ固定（`latest` やタグなしではない）かダイジェスト指定 |
| EFF-001 | efficiency | warning | CPU の request がある |
| EFF-002 | efficiency | warning | メモリの request がある |
| EFF-003 | efficiency | warning | メモリの limit がある |
| SEC-001 | security | **danger** | privileged ではない |
| SEC-002 | security | **danger** | hostNetwork / hostPID / hostIPC を使わない |
| SEC-003 | security | warning | root 以外で動く |
| SEC-004 | security | warning | allowPrivilegeEscalation が false |
| SEC-005 | security | warning | ルートファイルシステムが読み取り専用 |
| SEC-006 | security | warning | capabilities を ALL で drop している |
| SEC-007 | security | **danger** | SYS_ADMIN や NET_ADMIN などの危険な capability を追加しない |
| SEC-008 | security | warning | hostPath ボリュームを使わない |
| SEC-009 | security | warning | seccomp プロファイルが RuntimeDefault か Localhost |
| SEC-010 | security | warning | ServiceAccount のトークンを自動マウントしない |

danger は warning の 3 倍の重みでスコアに効きます。Job / CronJob にはプローブのチェックを適用しません。

## 使い方

```sh
# マニフェストを採点（ディレクトリは *.yaml / *.yml を再帰的に読む）
kube-readiness k8s/

# Helm チャートを採点
helm template my-release ./chart | kube-readiness -

# 稼働中のクラスタを採点（kube-system などは既定で除外）
kube-readiness --cluster --context prod

# CI のゲート: 80 点未満か danger があれば終了コード 1
kube-readiness --min-score 80 --fail-on-danger k8s/
```

出力例（[`examples/`](examples) に対する実行結果）:

```
SCORE  GRADE  WORKLOAD                            FAILED CHECKS
12     F      DaemonSet/readiness-bad/node-agent  SEC-001! SEC-002! SEC-007! REL-001 REL-002 EFF-001 EFF-002 EFF-003 SEC-003 SEC-004 SEC-005 SEC-006 SEC-008 SEC-009 SEC-010
40     F      Deployment/readiness-bad/api        REL-005! REL-007! REL-001 REL-002 REL-006 EFF-001 EFF-002 EFF-003 SEC-003 SEC-004 SEC-005 SEC-006 SEC-009 SEC-010
41     F      StatefulSet/readiness-bad/cache     REL-007! REL-001 REL-002 REL-003 EFF-001 EFF-002 EFF-003 SEC-003 SEC-004 SEC-006 SEC-009 SEC-010
100    A      Deployment/readiness-good/web       -

Score 50 (F)  reliability 48  efficiency 25  security 57  |  4 workloads, 6 danger, 35 warning
(! = danger. Run with --output markdown for details.)
```

[`examples/good.yaml`](examples/good.yaml) は全チェックに合格し、実際に kind で動く構成です。新しいワークロードの雛形としても使えます。

### 除外と重大度の変更

ワークロード単位では、アノテーションで除外できます。

```yaml
metadata:
  annotations:
    kube-readiness/ignore: SEC-005,REL-003
```

まとめて設定する場合は `--config` に YAML を渡します。

```yaml
checks:
  SEC-005: ignore      # 読み取り専用ルートFSはチームでは採用しない
  REL-006: danger      # 分散配置は必須にする
exemptions:
  - namespaces: ["monitoring"]
    kinds: [DaemonSet]
    checks: [SEC-002, SEC-008]   # node-exporter はホストのメトリクスを読む
```

| フラグ | 既定値 | 説明 |
|---|---|---|
| `--cluster` | `false` | ファイルではなく現在の kubeconfig のクラスタを読む |
| `--context` / `--kubeconfig` / `--namespace` | - | クラスタモードの接続先と対象 |
| `--exclude-namespace` | `kube-system,kube-public,kube-node-lease` | 対象外の namespace |
| `--config` | - | 重大度の変更と除外の設定 |
| `--output` | `table` | `table` / `json` / `markdown` / `sarif` |
| `--min-score` | `0` | 全体スコアがこれ未満なら終了コード 1 |
| `--fail-on-danger` | `false` | danger があれば終了コード 1 |

## インストール

[Releases](https://github.com/OTA-EITA/kube-readiness/releases) からバイナリをダウンロードするか、Go でインストールします。

```sh
go install github.com/OTA-EITA/kube-readiness/cmd/kube-readiness@latest
```

## テスト

- チェックとスコアの計算を、良い例・悪い例のマニフェストで検証（PDB の割合はディスラプションコントローラと同じく切り上げで計算）
- fake clientset によるクラスタモードのテスト
- CI の E2E: kind クラスタに例をデプロイし、`--cluster` で良い例が 100 点、悪い例で各 danger が検出されることを確認（[`e2e/run.sh`](e2e/run.sh)）

## License

MIT
