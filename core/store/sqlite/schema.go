package sqlite

// 表结构（data-contract §4.2，review-1 修订版）。
//
// 迁移内嵌：每个 owner（核心表 = "core"，插件 = 插件名）各有自己的版本号，
// 启动时按序执行未执行的迁移。迁移只追加，不改已发布的语句。

// migration：一个 owner 的一个版本。
type migration struct {
	version int
	stmts   []string
}

// coreMigrations：核心表。tenant 列单机恒为 "local"（data-contract D4）。
var coreMigrations = []migration{
	{version: 1, stmts: []string{
		`CREATE TABLE sessions(
			tenant TEXT NOT NULL, id TEXT NOT NULL PRIMARY KEY,
			agent_id TEXT NOT NULL DEFAULT '', agent_version INTEGER NOT NULL DEFAULT 0,
			parent_id TEXT NOT NULL DEFAULT '', origin TEXT NOT NULL DEFAULT 'new', fork_upto INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'active', run_state TEXT NOT NULL DEFAULT 'idle',
			title TEXT NOT NULL DEFAULT '', preview TEXT NOT NULL DEFAULT '',
			header TEXT NOT NULL, shell_hash TEXT NOT NULL DEFAULT '',
			log_len INTEGER NOT NULL DEFAULT 0, next_sync_seq INTEGER NOT NULL DEFAULT 1,
			updated_at INTEGER NOT NULL, deleted_at INTEGER NOT NULL DEFAULT 0)`,
		`CREATE INDEX sessions_list ON sessions(tenant, status, updated_at)`,
		`CREATE INDEX sessions_parent ON sessions(parent_id)`,

		`CREATE TABLE session_shells(hash TEXT NOT NULL PRIMARY KEY, body TEXT NOT NULL, created_at INTEGER NOT NULL)`,

		`CREATE TABLE session_entries(
			tenant TEXT NOT NULL, session_id TEXT NOT NULL, idx INTEGER NOT NULL,
			id TEXT NOT NULL, turn INTEGER NOT NULL, kind TEXT NOT NULL,
			summary TEXT NOT NULL DEFAULT '', body TEXT NOT NULL, at INTEGER NOT NULL, sync_seq INTEGER NOT NULL,
			PRIMARY KEY(tenant, session_id, idx))`,
		`CREATE INDEX session_entries_turn ON session_entries(session_id, turn)`,
		`CREATE INDEX session_entries_seq ON session_entries(session_id, sync_seq)`,

		`CREATE TABLE session_entries_archive(
			tenant TEXT NOT NULL, session_id TEXT NOT NULL, idx INTEGER NOT NULL, id TEXT NOT NULL,
			body TEXT NOT NULL, truncated_at INTEGER NOT NULL, rewind_seq INTEGER NOT NULL)`,
		`CREATE INDEX session_entries_archive_s ON session_entries_archive(session_id, rewind_seq)`,

		`CREATE TABLE session_requests(
			tenant TEXT NOT NULL, session_id TEXT NOT NULL, request_id TEXT NOT NULL,
			shell_hash TEXT NOT NULL, prefix_base INTEGER NOT NULL, prefix_end INTEGER NOT NULL,
			compaction_view TEXT NOT NULL DEFAULT '', suffix TEXT NOT NULL DEFAULT '',
			final_hash TEXT NOT NULL, composition TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL,
			PRIMARY KEY(tenant, session_id, request_id))`,

		`CREATE TABLE session_inbox(
			tenant TEXT NOT NULL, session_id TEXT NOT NULL, item_id TEXT NOT NULL,
			kind TEXT NOT NULL, input TEXT NOT NULL, trust TEXT NOT NULL DEFAULT 'trusted', created_at INTEGER NOT NULL,
			PRIMARY KEY(tenant, session_id, item_id))`,

		`CREATE TABLE tool_results(
			tenant TEXT NOT NULL, session_id TEXT NOT NULL, call_id TEXT NOT NULL,
			status TEXT NOT NULL, kind TEXT NOT NULL DEFAULT '', args_hash TEXT NOT NULL DEFAULT '',
			decision TEXT NOT NULL DEFAULT '', delivered TEXT NOT NULL DEFAULT '', output TEXT NOT NULL DEFAULT '',
			deadline INTEGER NOT NULL DEFAULT 0, token_hash TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL,
			PRIMARY KEY(tenant, session_id, call_id))`,
		`CREATE INDEX tool_results_deadline ON tool_results(status, deadline)`,

		`CREATE TABLE trace_spans(
			tenant TEXT NOT NULL, session_id TEXT NOT NULL, span_id TEXT NOT NULL PRIMARY KEY,
			parent_span TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL, name TEXT NOT NULL DEFAULT '',
			started_at INTEGER NOT NULL, ended_at INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT '',
			attrs TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX trace_spans_s ON trace_spans(session_id, started_at)`,

		`CREATE TABLE blobs(sha256 TEXT NOT NULL PRIMARY KEY, size INTEGER NOT NULL, mime TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL, touched_at INTEGER NOT NULL)`,
		`CREATE TABLE blob_refs(tenant TEXT NOT NULL, session_id TEXT NOT NULL, sha256 TEXT NOT NULL,
			PRIMARY KEY(tenant, session_id, sha256))`,

		`CREATE TABLE definitions(tenant TEXT NOT NULL, kind TEXT NOT NULL, id TEXT NOT NULL, version INTEGER NOT NULL,
			body TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY(tenant, kind, id, version))`,
	}},
	// v2：inbox 消费改为标记（session-host D6：已投递的 item id 重放仍幂等）。
	{version: 2, stmts: []string{
		`ALTER TABLE session_inbox ADD COLUMN consumed_at INTEGER NOT NULL DEFAULT 0`,
	}},
	// v3：底座 T1（底座设计 05 5.9、09 9.2）。新增 agent_hash 存定义整包内容哈希，旧 agent_version 整数列保留不用（迁移只加列，旧代码可读新库）；
	// 补创建者、租约（持有者 + 纪元，T2 启用 fencing）与会话元数据。
	{version: 3, stmts: []string{
		`ALTER TABLE sessions ADD COLUMN agent_hash TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN creator TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN lease_holder TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN lease_epoch INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sessions ADD COLUMN meta TEXT NOT NULL DEFAULT '{}'`,
		// 会话键（框架设计 13 13.3，B19 修订）：不带 key 存 NULL，不参与唯一约束。
		`ALTER TABLE sessions ADD COLUMN session_key TEXT`,
		`CREATE UNIQUE INDEX sessions_key ON sessions(tenant, agent_id, creator, session_key) WHERE session_key IS NOT NULL`,
		// 使用配置（框架设计 06，D8）：创建会话时算好存入。
		`ALTER TABLE sessions ADD COLUMN use_config TEXT NOT NULL DEFAULT '{}'`,
		// 定义整包内容哈希（D1）；version 整数列保留作排序用。
		`ALTER TABLE definitions ADD COLUMN hash TEXT NOT NULL DEFAULT ''`,
	}},
	// v4：T3 信封与待回复表（11 信封表 / 11.2 待回复表 / 13 13.3）。
	// inbox 增加 envelope 列存整封 JSON（旧行留空，按纯文本处理）；session_pending 不复用
	// session_requests（那张是模型请求日志）。
	{version: 4, stmts: []string{
		`ALTER TABLE session_inbox ADD COLUMN envelope TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE session_pending(
			tenant TEXT NOT NULL, corr TEXT NOT NULL PRIMARY KEY,
			session_id TEXT NOT NULL, reply_to TEXT NOT NULL DEFAULT '', trace TEXT NOT NULL DEFAULT '',
			issued_idx INTEGER NOT NULL DEFAULT 0, deadline INTEGER NOT NULL DEFAULT 0,
			used INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL)`,
		`CREATE INDEX session_pending_s ON session_pending(session_id, issued_idx)`,
	}},
}
