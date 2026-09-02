# WorkOS

WorkOS 是运行在 Linux 之上的个人工作操作环境。人、Agent 与长期运行的软件围绕
Project 协作；Harness、App、Surface、Workload 与 Incident 均通过稳定协议接入。

> 当前处于 foundation 阶段。产品愿景见 [docs/structure.md](docs/structure.md)，当前可运行
> 能力以本文的状态表和 [docs/status.json](docs/status.json) 为准。

## 当前状态

<!-- status:start -->

最后更新：2026-08-31

<!-- prettier-ignore -->
| 模块 | 进程 | 状态 | 证据 |
| --- | --- | --- | --- |
| Access Gateway | workos-gateway | `working` | core+runtime+reliability upstreams, surface routing, dynamic session-derived identity injection; production device auth on a direct configured HTTPS origin: real TLS gateway + PostgreSQL + controlled admin Unix socket ticket + real Chromium pairing (non-extractable WebCrypto P-256 proof with IndexedDB key-pair self-check), explicit proof version/purpose, __Host- HttpOnly cookie, per-request session gate with dynamic owner/device injection, per-remote plus global anonymous budgets, corruption-to-Internal validation, gateway-restart session persistence, session-proof re-authentication, and revocation fail-closed (make test-lan-pairing; deterministic before/after visual records). Not covered: mDNS discovery, native certificate pinning/trust-store bypass-free ACME, mobile-native credential storage, public-internet exposure |
| Project | workos-core | `working` | revision-safe server-preset binding integration + browser E2E + contract hardening: pre-decode wire budget on public ProjectService RPCs + bounded application validation (name/icon/refs/binding limits with C0/C1 rejection, UUIDv7 id/cursor grammar, ambiguous-update rejection) + durable create idempotency with canonical request digest and versioned first-response snapshot in project_create_requests (013, NULL-safe version CHECK): same key/same request replays the exact first response across restarts after fail-closed snapshot invariant validation (owner match, canonical UUIDv7, revision 1, never archived, field grammars), different request is stable Aborted, failures never consume the key, legacy keys fail closed (real PostgreSQL concurrency + commit/transient-failure rollback evidence) + application-owned pagination with exact last-page tokens + sanitized fixed error matrix with real-pgx Unavailable mapping |
| Harness Provider Catalog | workos-core | `working` | public Catalog integration + default/DeepSeek fixture browser E2E |
| Credential Vault | workos-core | `working` | provider API-key encrypted store + local admin Unix socket + task-bound leases only (ADR-0009, migrations 023/024): one no-symlink-follow descriptor reads an absolute owner-only 32-raw-byte master file, domain-separated child keys drive AES-256-GCM and keyed idempotency digests, put/rotate/revoke all physically arbitrate idempotency before mutation, and stored metadata is revalidated fail-closed; server-derived opaque credential_ref in HarnessBinding, exact per-task (credential_id, revision) snapshots resolved pre-enqueue and re-verified at approval, AcquireTaskCredential derived solely from the active task lease over the mutual TLS 1.3 harness execution listener (single exact URI SAN identity), response-loss replay/renew/release/revocation/expiry convergence, DeepSeek runs only on a live lease with the legacy DEEPSEEK_API_KEY env path retired; dev Compose and production systemd isolate Core server identity/vault key from the Harness client identity and every sibling process (make test-credential-vault missing/granted/revoked phases on real PostgreSQL + workosctl admin socket + local DeepSeek fixture). Not covered: OAuth/Codex/GitHub/cloud credentials, secret reveal/export UI, master-key online rotation, multi-owner catalog overlay, mDNS/public exposure |
| Event Backbone | workos-core | `working` | persisted ordered stream + resume integration |
| Agent Task Router | workos-core | `working` | Project binding snapshot + user idempotency + project-scoped App principal/provenance with durable (owner, app_instance, client key) digest adjudication (real PostgreSQL concurrency + restart) + Agent-owned per-installation policy (full replacement, owner-scoped idempotency with versioned first-response snapshot, optimistic policy revision, finite versioned system default v1, pending-approval invalidation on real change linearized against waiting-approval creation via a per-installation policy-chain advisory lock plus in-transaction approval-snapshot re-verification with zero-consumption rollback, stored-row corruption validated on every read — spec grammar, revision, digest, and the project binding against the active installation's project — and surfaced as sanitized internal corruption, never overwritten silently and never silently re-bound) + fresh App run adjudication (replay-first, provider hard_token_budget/hard_runtime_deadline/usage_reporting verification including the provider's enforced per-task budget maxima so over-bound policies fail closed before any queue slot or reservation, allow=atomic task+provenance+guarded UTC-daily reservation+outbox, require_approval=atomic waiting task+pending approval+approval_required event with no outbox/reservation, block/quota-exhausted/breach fail closed without consuming the run key) + owner-only pre-run approvals (public list/get/decide with decision idempotency, InvalidArgument on unknown state filters, and limit+1 probed next-page tokens that never phantom-page on a full final page, exactly-one-winner concurrent decisions, approve revalidates installation/grant/policy revision/provider capability and maxima then atomically reserves + queues + outboxes, reject never revalidates so it stays decidable after uninstall/grant revocation/provider loss, reject terminates without reservation) + usage projection with deterministic breach circuit break (same-transaction usage_recorded accumulation with NULL-safe cost that starts at the first known observation and sums across events and tasks, breach terminates the task cancelled with exactly one run_cancelled system event in the same transaction so no provider completion can land afterwards and the outbox request is finished, subsequent fresh runs fail closed; real PostgreSQL concurrency + restart evidence) + worker-enforced server-derived runtime deadline that abandons cancellation-ignoring providers after a bounded grace with exactly one terminal event and a finished lease, and terminal events counted only once durably appended so a lost terminal write is repaired with the deterministic fallback before finishing; fresh user tasks requesting artifact outputs are verified pre-enqueue against the resolved provider's exact supported list (canonical markdown/diff types only, no duplicates, at most two), unsupported providers fail closed with FailedPrecondition and zero task/outbox/lease side effects, and global-scope tasks cannot request project review outputs (ADR-0008) |
| Harness Broker | harness-host | `working` | Fake, Generic CLI, and typed provider execution tests; Fake/DeepSeek declare their enforced per-task budget maxima alongside the hard budget contract; Fake provider structured artifact output (exact supported_artifact_types list, deterministic bounded documents, exactly one per requested type before the terminal) drives the private lease-bound AppendTaskArtifact protocol end to end (ADR-0008) |
| DeepSeek Harness Adapter | harness-host | `working` | official runtime + keyless streaming fixture integration and browser E2E; since ADR-0009 the adapter holds no long-lived key: every run requires a task-bound credential lease from the Core Credential Vault (requires_task_credential_lease), the legacy DEEPSEEK_API_KEY env path is retired with a sanitized migration issue, and the lease secret reaches only that task's allowlisted child env; structured review (ADR-0011) with structured_artifacts=true and exact markdown/diff outputs: strict single-JSON parser (duplicate keys, trailing content, unknown fields, bounds, control rejection), adapter-derived keys/titles, raw stream suppressed from the timeline, atomic two-output batch materialization on real PostgreSQL, and malformed-output fail-closed (make test-deepseek-structured-review over PostgreSQL + Core mTLS + harness-host + workosctl admin socket + local fixture + Chromium) |
| Desktop Shell | desktop-web | `working` | foundation + DeepSeek fixture Catalog/binding E2E + App Library install/remove + explicit permission consent E2E + Manage permissions dialog for full grant replacement (revoke/re-grant browser E2E reopens on the new epoch; before/after visual records) + sandboxed Web Bundle window with opaque-origin MessageChannel App Bridge (browser E2E runs a real project task) + unmount best-effort close + library row and project revision adopt the Set response even when the post-save re-read fails (fault-injected before/after visual record) + App Library Agent policy editor (system default/explicit display, full-replacement mode+limits save with optimistic revision, sanitized conflict/quota copy) + Agent Center Approvals/Usage views (owner approve/reject with duplicate-click guard and project-switch-safe decision feedback, reserved vs reported usage with unavailable cost; browser E2E drives require-approval → waiting → approve → same task terminal → usage, and the deterministic quota-exhausted verdict; before/after visual records) + Auth Gate state machine (unpaired/pairing/session-proof/unavailable) with fragment scrub and WebCrypto/IndexedDB profile key binding self-check + Device Center window (paired-device list, session expiry, expiring pair-another-device QR, retry-stable revoke idempotency, logout) browser-tested through the TLS pairing gate (deterministic visual records); Artifact Center window (paged project review list with loading/empty/unavailable/retry states, generation-guarded project switching) + styled removable Agent context chips + read-only inert Markdown/unified-diff Artifact Review windows + clickable Core-minted artifact timeline events, all reading the authoritative ArtifactService through the real fake-harness chain (deterministic before/after/current visual records) + adaptive shell integration: the desktop-web window/project state now renders through the shared adaptive contract in compact, medium, and fold-separated modes (single main pane via a pure window projection, bottom navigation, project sheet with project settings, full-screen App Library overlay, Agent slide-over with Escape dismissal, explicit dock, fold dual-pane with non-interactive hinge and persisted single/dual preference) while the expanded branch keeps the original free-window desktop pixel-for-pixel; device-local layout state is persisted per project + device class, transaction-serialized and sanitized per key, with a persistent session fallback when IndexedDB is unavailable, authoritative full-page Project sweeps, and uninstall/grant/version/session-end cleanup (make test-adaptive-shell multi-viewport browser gate + Desktop.adaptive unit tests) + the Versions dialog immediately adopts authoritative version/revision responses and the incident-scoped rollback entry in System Monitor caches authoritative no-history results, invalidates installation surfaces, and updates the parent project revision (ADR-0012 evidence in Project App Installation) |
| App Registry | workos-core | `working` | schema-backed immutable registration + durable idempotency + bounded paging/read + credential-shaped key rejection + restart persistence |
| Project App Installation | workos-core | `working` | pinned version install/uninstall + explicit canonical grant set (subset of requested) now replaceable via SetAppGrants full replacement with grant revision epoch + single-transaction revision/event/outbox + deterministic no-op that still consumes its key + durable idempotency with grant/revision result snapshot (real PostgreSQL concurrency + restart) + Desktop consent & manage-permissions E2E + review hardening: pre-decode wire budget on public installation RPCs (oversize/gzip bombs ResourceExhausted before business code) + real-pgx transient outages surface as retryable Unavailable end to end + owner-triggered version transition and previous-pinned-version rollback (ADR-0012, migration 025): server-resolved exact registry target with digest re-verification, current-grants-must-be-coverable fail-closed (permissions never expand, rollback never widens), immutable-entry bounded (20) per-installation version history seeded at install and revalidated on every read, canonical UUIDv7/UTC-microsecond stored projections and newest-history-tail consistency, same-namespace idempotency with result_version/result_manifest_digest first-response snapshots whose complete overlaid installation is revalidated (same key replays exactly across restarts, different request stable Aborted, failures never consume the key), project revision serialization under the same row lock, and project.app.version.updated.v1 events; Desktop Versions dialog (history + explicit switch consent + rollback preview) and System Monitor incident-scoped rollback entry with distinct completed/no-previous/permissions-review/conflict copy (make test-app-version-rollback over PostgreSQL + Core + Gateway + Chromium plus real-PostgreSQL integration tests and the restart battery) |
| Artifact | workos-core | `working` | two immutable subtypes with full real-stack evidence: web bundle (owner-scoped launch payload, bounded upload, canonical digest, durable idempotency, private assets — never public bytes) and project review artifacts (ADR-0008, migrations 021/022): canonical document.markdown.v1 and code.unified-diff.v1 materialized through the private lease-bound AppendTaskArtifact RPC — Core derives owner/project/task from the active task lease only, normalizes and bounds Unicode content (CRLF→LF, actual NUL/C0/C1 rejection, ≤512 KiB/20k lines/16 KiB per line), mints UUIDv7/digest/UTC-microsecond time, adjudicates the durable (task, output key) identity in one transaction with the Core-minted ArtifactCreated timeline event (full Artifact-owned binding constraints, concurrency, response-loss replay, type-slot conflicts, lease-loss and restart convergence on real PostgreSQL), verifies replay against the actual durable Agent-owned publication, and never trusts provider-built artifact references (generic AppendTaskEvent fails closed); owner/project-scoped ListArtifacts, metadata Get and typed GetReviewArtifact revalidate Project binding plus canonical content/digest/count from an authoritative snapshot (corruption→Internal, transient→Unavailable, web bundle→not-reviewable), with pre-decode 32 KiB public read and 768 KiB private materialization budgets; read-only inert Markdown/unified-diff windows validate response bindings and fatal UTF-8, while Artifact Center/clickable timeline events are isolated across Project switches (make test-artifact-review real cross-process Chromium gate + deterministic before/after/current visual records + real-PostgreSQL integration tests); Fake provider declares exact supported artifact types with deterministic output, DeepSeek/Generic CLI honestly report unsupported; generic artifact storage beyond these subtypes unimplemented; review artifacts additionally serve as the first canonical Agent context (ADR-0010): context refs (artifact.review.v1 + UUIDv7 + exact sha256 digest, max 4, order-preserving, no duplicates) are grammar- and capability-checked and existence/owner/project/digest-verified pre-enqueue with zero side effects, materialized for the harness worker only through the private lease-bound ResolveTaskContext inside one transaction (byte-identical replay, foreign/digest/lost-lease fail closed, 1 MiB aggregate bound), and the desktop pins them as removable composer chips with deterministic before/after/current visual records (make test-artifact-context) |
| Runtime / Surface | runtime-host | `working` | Web Bundle surfaces only (working): durable idempotent device-bound sessions + per-request Core revalidation + token-validated minimal App Bridge (agent.task.run / agent.event.watch only, 256-bit token, sha256 at rest, rotation + restart persistence) + sessions persist the installation grant revision and Core re-compares it every run/watch round; supervised web-service container slice remains scaffolded behind ADR-0006: Workload Manager migrations 015/018 persist target-generation and idle_since convergence facts; state transitions and terminal operation verdicts commit atomically, terminal verdicts are immutable, restart generations are adjacent, and exact-owned shutdown converges profile drift while identity mismatches remain untouched; Podman adoption verifies full identity, exact JSON exec-form argv, one declared loopback publish, actual empty effective/bounding capability sets, and the remaining immutable security profile; pids.events max is observed through an additive protocol field, and the read-only proxy revalidates exact Core kind/app/version/digest plus workload generation, rejects encoded or over-budget responses before committing a backend status, and validates the final loopback target; fake-engine/adapter/PostgreSQL tests pass, but there is still no real rootless Podman or cross-process browser evidence on an acceptance host, so container/native runners remain unavailable |
| Reliability | reliability-host | `scaffolded` | health + supervision loop implemented but capability remains unavailable: migrations 016/017/019, occurrence-deduped incidents, owner/project-scoped public IncidentService including cursor boundaries, bounded monotonic action ledger (only unavailable retries; failed/unsupported/limit outcomes are terminal), generation-independent pending-action crash recovery, strict mitigated->resolved lifecycle, old-generation recovery resolution, and distinct unsupported vs restart-limit control outcomes have fake-driven and PostgreSQL evidence; there is still no real observation->incident->restart/stop cross-process evidence, so supervisor and incident-manager report false; repair-orchestrator/deployment remain unavailable |
| Indexer | indexer | `working` | project review-artifact durable lexical index/search (ADR-0013, migrations 026-028): Core appends content-free publications in the same transaction as immutable review artifacts and project archive tombstones; its private source service supplies leased at-least-once claims and bounded authoritative reconciliation. Indexer owns generation-scoped documents, receipts, cursor, repair/rebuild jobs and project tombstones; document+receipt+cursor commit before Core completion, and any durable tombstone permanently wins over late upserts. Owner search through Gateway uses Unicode-aware bounded lexical input, deterministic score/ordering/excerpts, and Indexer-keyed HMAC page tokens bound to owner/project/query/ranking/generation/snapshot; make test-project-knowledge-search proves the real Core/Indexer/Gateway/Chromium context journey, exact signed-snapshot pagination, archive convergence, deliberate Indexer outage and recovery. A granted opaque App reaches the same scoped projection only after exact current grant-revision authorization; denial has zero Indexer calls and make test-app-knowledge-search proves grant/revoke behavior. The local mode-0600 admin socket and workosctl drive a durable shadow rebuild: request facts and promotion facts commit atomically, only one global live rebuild is database-allowed, live effects mirror to active+building generations, every phase survives a fresh executor, response-loss promotion replays safely, and retired payload cleanup is bounded; make test-project-knowledge-rebuild combines a real Core/Indexer/admin restart journey with >200-row PostgreSQL golden, concurrent live upsert/archive, idempotency/conflict/cancel, and temporary Indexer-schema destroy/restore evidence. Knowledge Center and granted-App UI have deterministic before/after/current records. Not covered: generic archive/object store, semantic RAG/embeddings/pgvector, workspace/file sources, or multi-owner deployments. |
| Mobile Shell | mobile-shell | `working` | shared adaptive device contract + real responsive shell (docs/structure.md 12.2-12.6): @workos/adaptive-shell owns the single DeviceClass-to-layout mapping and a pure, DOM-free layout derivation (compact/medium/expanded/fold-separated with boundary and posture tests at 599/600 and 1023/1024, hostile DPR normalization, validated and canonicalized two-segment fold posture with horizontal/vertical hinge gaps, safe degradation without segments) plus the device-local layout store (origin-scoped IndexedDB keyed per project + device class, schema-versioned sanitized records holding only bounded canonical UUIDv7 references and the single/dual pane preference, transaction-serialized writes, per-key corruption reset, authoritative project sweeps and installation/session cleanup, persistent session fallback when IndexedDB is unavailable) and the window projection; the Desktop renders all four modes over the same window/project state with a bottom nav and project sheet on phone, an Agent slide-over and explicit dock on tablet, dual panes only when the host really reports two window segments, and the unchanged free-window desktop at expanded widths; PWA manifest with static caching boundary (hashed assets immutable, HTML/manifest no-store). Evidence: make test-adaptive-shell (real Gateway/Core/harness/runtime/Chromium at 390x844, 820x1180, 1440x900 plus an injected fold-segment fixture: project creation, App Library install, fake-harness task, System Monitor, slide-over/dock behavior, dual/single pane, expanded regression, reload-persistent layout state), deterministic multi-viewport visual records (docs/ui/desktop-web/changes/20260831-adaptive-shell/), and 40 contract/storage/hook unit tests. Not covered: Capacitor iPad/Android wrappers, push, native secure storage, fold hardware on a real device |

<!-- status:end -->

状态含义：`contract-only` 只有稳定契约；`scaffolded` 可构建和探活；`working` 有真实链路；
`verified` 具有持续运行的集成/E2E 证据。任何占位实现都不能标记为 working。

## 架构

```text
Desktop / SDK
      │
workos-gateway ── identity / capability / public API
      │
workos-core ───── Project / Agent API / Event Backbone
      ├────────── harness-host
      ├────────── runtime-host
      ├────────── reliability-host
      └────────── indexer
                    │
                PostgreSQL
```

六个宿主是稳定进程边界，不能为了方便把它们合并成单进程。详细所有权与依赖规则见
[当前实现架构](docs/architecture/implementation.md)。

## 快速开始

前置条件：Docker 29+、GNU Make 4+。本机不需要预装 Go、Node、Buf 或 PostgreSQL。
Go、npm/pnpm、Debian 与 Playwright 下载默认使用国内镜像，且均可通过环境变量覆盖；详见
[部署说明](deploy/README.md)。

```bash
make bootstrap
make generate
make dev
```

打开 <http://127.0.0.1:8080>。开发模式仅允许绑定 loopback，并注入固定的 owner/device
身份；它不能用于局域网部署。

常用命令：

```bash
make check             # 与 CI 相同的静态检查和单元测试
make test-integration  # PostgreSQL、事件与进程集成测试
make test-deepseek-fixture # 官方 DeepSeek Harness + 本地无密钥 API fixture
make test-e2e          # Desktop → Project → Agent Task 浏览器链路
make logs              # 查看开发栈日志
make down              # 停止开发栈
```

## 配置与运行

- 默认开发配置在 `deploy/config/dev.yaml`，可复制后通过 `WORKOS_CONFIG_FILE` 指定。
- Secret 只能通过环境变量或受限文件引用注入，禁止写入 YAML、日志和 App 响应。
- `deploy/systemd` 提供本机服务管理骨架。默认开发模式（dev bypass）仍只允许 loopback；
  生产 LAN 配对需要 operator 提供的可信 TLS 证书与 Gateway 自终止 TLS（ADR-0007，见
  [部署说明](deploy/README.md)），mDNS 发现、native pinning 与公网访问仍不在范围内。
- 自定义 workload 的 rootless Podman runner 尚未实现，Runtime 目前只做只读 capability probe。
- `workosctl doctor` 检查配置、PostgreSQL、cgroup v2 和内部服务连接。
- 设置 `OTEL_EXPORTER_OTLP_ENDPOINT` 即可启用六个进程的 OTLP/HTTP trace；示例见
  [部署说明](deploy/README.md)。

### Project Harness 设置

Desktop 的 Project settings 从 Gateway 查询 canonical Provider Catalog，并显示 provider stable ID、
adapter version、真实 health 与 capability。浏览器只调用 Core 的只读 `HarnessCatalogService`；包含
`ExecuteTask` / `CancelRun` 的 `HarnessHostService` 仍是进程间私有接口，Gateway 不转发它。

Project 可以选择 healthy 或 degraded provider，也可以清除 binding 以使用 Core 的 global default。
starting、unavailable 与 unknown provider 不允许新绑定；已经存在但后来不可用或不再出现在 Catalog
中的 binding 仍会显示并可解除。保存使用 Project revision 做乐观并发控制，Task 页面显示 Submit 时
持久化的 `provider_id` 快照，Project 后续改绑不会改变旧 Task。

客户端不提供 Key、credential ref 或内部 policy 输入。Core 为新 binding 注入非 secret 的
`instance_policy` / `profile_id` / `resource_policy_id` preset；当前 resource policy 只是 binding reference，
尚不代表 cgroup 或其他资源强制已经实现。Catalog 是可选能力，harness-host 暂时不可达不会影响
Project CRUD、Core readiness 或已有 Task。

### DeepSeek Harness

DeepSeek Provider 默认关闭；有 Key 也不会隐式启用。启用时凭据只来自 Core
Credential Vault 的 task-bound 短期 lease（`workosctl credential put` 经
Core admin Unix socket 写入），非 secret 参数使用 `WORKOS_DEEPSEEK_*` 环境变量；
`DEEPSEEK_API_KEY` 已废弃，被设置时只会得到迁移指引而不是被读取。
容器固定并校验官方 Harness runtime，常规 CI 只运行本地 fixture，不访问 DeepSeek 网络。
`make test-deepseek-fixture` 还会从浏览器选择 DeepSeek、验证 Project revision、Task provider snapshot、
ordered events 与改绑语义；它只使用 loopback API fixture 和明确的假 credential。
配置、输入限制、测试方法和真实 API smoke 的限制见
[adapter 说明](internal/harness/adapters/deepseek/README.md)。

## 仓库地图

| 路径                       | 责任                                       |
| -------------------------- | ------------------------------------------ |
| `api/proto`                | 跨进程、Go/TypeScript 的 v1 事实契约       |
| `internal/core`            | Project、Catalog、binding、Task 与事件主干 |
| `internal/harness`         | Broker 与 provider adapters                |
| `internal/platform`        | identity、配置、迁移、日志、遥测等共享机制 |
| `apps` / `clients` / `sdk` | Shell、可复用客户端模块与公共 SDK          |
| `deploy`                   | Compose、OTel 与 systemd 资产              |
| `docs/tasks`               | 可跨智能体交接的任务事实记录               |

新增业务模块时不要手抄目录：

```bash
make scaffold-module PROCESS=workos-core NAME=calendar
```

脚手架只建立 `domain/application/ports/adapters/transport` 边界，不会生成虚假的业务实现，也不会
覆盖已有目录。

## 参与开发

开始任何工作前必须阅读 [AGENTS.md](AGENTS.md) 与
[CONTRIBUTING.md](CONTRIBUTING.md)。新任务从 [任务模板](docs/tasks/TEMPLATE.md) 建立独立
记录；改变进程边界、模块所有权或 v1 协议必须新增 ADR。

本项目使用 [Apache License 2.0](LICENSE)。
