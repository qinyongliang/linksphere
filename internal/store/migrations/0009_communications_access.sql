-- Explicit consent is separate from target visibility, desktop access and MCP groups.
-- No grants are created by migration or inherited from an administrator role.
CREATE TABLE communications_access (
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 target_id TEXT NOT NULL REFERENCES ssh_targets(id) ON DELETE CASCADE,
 operation TEXT NOT NULL CHECK(operation IN ('call','audio','sms')),
 allowed INTEGER NOT NULL DEFAULT 0 CHECK(allowed IN (0,1)),
 expires_at TEXT,
 granted_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(user_id,target_id,operation)
);
CREATE INDEX communications_access_target ON communications_access(target_id);
