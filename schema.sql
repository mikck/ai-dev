CREATE TABLE IF NOT EXISTS workspaces (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE);
CREATE TABLE IF NOT EXISTS repositories (
    id TEXT PRIMARY KEY, workspace TEXT NOT NULL REFERENCES workspaces,
    provider TEXT NOT NULL, url TEXT NOT NULL, base TEXT NOT NULL, verify TEXT NOT NULL,
    UNIQUE(workspace, url)
);
CREATE TABLE IF NOT EXISTS requirements (
    id TEXT PRIMARY KEY, workspace TEXT NOT NULL REFERENCES workspaces,
    title TEXT NOT NULL, description TEXT NOT NULL, engine TEXT NOT NULL,
    status TEXT NOT NULL, plan TEXT, log TEXT
);
CREATE TABLE IF NOT EXISTS targets (
    requirement TEXT NOT NULL REFERENCES requirements,
    repository TEXT NOT NULL REFERENCES repositories,
    review TEXT, PRIMARY KEY(requirement, repository)
);
CREATE TABLE IF NOT EXISTS resources (
    id TEXT PRIMARY KEY, workspace TEXT NOT NULL REFERENCES workspaces,
    kind TEXT NOT NULL, name TEXT NOT NULL, config TEXT NOT NULL,
    UNIQUE(workspace, kind, name)
);
CREATE TABLE IF NOT EXISTS sops (
    id TEXT PRIMARY KEY, workspace TEXT NOT NULL REFERENCES workspaces,
    name TEXT NOT NULL, nodes TEXT NOT NULL, UNIQUE(workspace, name)
);
CREATE TABLE IF NOT EXISTS executors (
    id TEXT PRIMARY KEY, name TEXT NOT NULL, proof TEXT NOT NULL,
    seen REAL NOT NULL, enabled INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS runs (
    requirement TEXT PRIMARY KEY REFERENCES requirements,
    snapshot TEXT NOT NULL, executor TEXT REFERENCES executors
);
CREATE TABLE IF NOT EXISTS steps (
    requirement TEXT NOT NULL REFERENCES runs, node TEXT NOT NULL, status TEXT NOT NULL,
    lease TEXT UNIQUE, artifact TEXT, PRIMARY KEY(requirement, node)
);
CREATE TABLE IF NOT EXISTS drafts (
    id TEXT PRIMARY KEY, workspace TEXT NOT NULL REFERENCES workspaces,
    engine TEXT NOT NULL, messages TEXT NOT NULL, status TEXT NOT NULL,
    executor TEXT REFERENCES executors, lease TEXT UNIQUE, nodes TEXT, error TEXT
);
CREATE TABLE IF NOT EXISTS legacy_jobs (
    requirement TEXT PRIMARY KEY REFERENCES requirements,
    executor TEXT NOT NULL REFERENCES executors, lease TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS sop_versions (
    sop TEXT NOT NULL REFERENCES sops ON DELETE CASCADE,
    version INTEGER NOT NULL, snapshot TEXT NOT NULL, published REAL NOT NULL,
    PRIMARY KEY(sop, version)
);
CREATE TABLE IF NOT EXISTS step_attempts (
    id INTEGER PRIMARY KEY, requirement TEXT NOT NULL, node TEXT NOT NULL,
    status TEXT NOT NULL, artifact TEXT NOT NULL, finished REAL NOT NULL,
    FOREIGN KEY(requirement, node) REFERENCES steps(requirement, node)
);
CREATE INDEX IF NOT EXISTS step_attempts_requirement ON step_attempts(requirement, node, id);
CREATE TABLE IF NOT EXISTS migrations (name TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS archived_repositories (
    id TEXT PRIMARY KEY REFERENCES repositories ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS archived_sops (
    id TEXT PRIMARY KEY REFERENCES sops ON DELETE CASCADE,
    name TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS target_settings (
    requirement TEXT NOT NULL, repository TEXT NOT NULL,
    provider TEXT NOT NULL, url TEXT NOT NULL, base TEXT NOT NULL, verify TEXT NOT NULL,
    PRIMARY KEY(requirement, repository),
    FOREIGN KEY(requirement, repository) REFERENCES targets(requirement, repository)
);
