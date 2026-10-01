package platform

import "github.com/ansoraGROUP/dupabase/internal/database"

// BackupMaintenanceMigration preserves legacy settings rows. Organization-row
// locks serialize future saves; readers select the most recently saved row.
var BackupMaintenanceMigration = database.Migration{
	Name: "010_backup_scope_and_provenance.sql",
	SQL: `
ALTER TABLE platform.backup_settings DROP CONSTRAINT IF EXISTS backup_settings_user_id_key;
CREATE INDEX IF NOT EXISTS idx_backup_settings_org_updated ON platform.backup_settings(org_id, updated_at DESC, id);
ALTER TABLE platform.backup_history ADD COLUMN IF NOT EXISTS content_sha256 TEXT;
ALTER TABLE platform.backup_history ADD COLUMN IF NOT EXISTS source_endpoint TEXT;
ALTER TABLE platform.backup_history ADD COLUMN IF NOT EXISTS source_region TEXT;
ALTER TABLE platform.backup_history ADD COLUMN IF NOT EXISTS source_bucket TEXT;
`,
}
