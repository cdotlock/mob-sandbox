package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/cdotlock/mob-sandbox/pkg/config"
	"github.com/cdotlock/mob-sandbox/pkg/remote"
)

type sshRunner struct {
	client *remote.SSHClient
}

func newSSHRunner(host string, port int, keyPath string) (*sshRunner, error) {
	keyPath = os.ExpandEnv(keyPath)
	client, err := remote.NewSSHClient(host, port, "root", keyPath)
	if err != nil {
		return nil, err
	}
	return &sshRunner{client: client}, nil
}

func newSSHRunnerFromConfig(cfg *config.ServerConfig) (*sshRunner, error) {
	keyPath := os.ExpandEnv("$HOME/.ssh/id_ed25519")
	return newSSHRunner(cfg.PublicIP, 22, keyPath)
}

func (s *sshRunner) Run(cmd string) (string, error) {
	return s.client.Run(cmd)
}

func (s *sshRunner) Upload(content []byte, path string) error {
	return s.client.Upload(content, path)
}

func (s *sshRunner) Close() error {
	return s.client.Close()
}

func insertAPIKey(ssh *sshRunner, name, key string) error {
	hash := sha256.Sum256([]byte(key))
	keyHash := hex.EncodeToString(hash[:])
	keyPrefix := key[:8]
	keySuffix := key[len(key)-4:]

	script := fmt.Sprintf(`
docker exec daytona-db psql -U daytona -d daytona <<'SQL'
DO $$
DECLARE
  org_id uuid;
  user_id text;
BEGIN
  SELECT id INTO org_id FROM organization LIMIT 1;
  SELECT id INTO user_id FROM "user" LIMIT 1;

  INSERT INTO api_key ("keyHash", "keyPrefix", "keySuffix", name, "createdAt", "organizationId", "userId", permissions)
  VALUES (
    '%s', '%s', '%s', '%s', NOW(), org_id, user_id,
    '{write:sandboxes,delete:sandboxes,write:snapshots,delete:snapshots,read:volumes,write:volumes,delete:volumes,read:runners,write:runners,read:audit_logs}'::api_key_permissions_enum[]
  )
  ON CONFLICT ("keyHash") DO NOTHING;

  UPDATE organization
  SET
    max_cpu_per_sandbox = 2,
    max_memory_per_sandbox = 4,
    max_disk_per_sandbox = 20,
    volume_quota = 100,
    "defaultRegionId" = COALESCE("defaultRegionId", 'us'),
    "updatedAt" = NOW()
  WHERE id = org_id;

  INSERT INTO region_quota (
    "organizationId", "regionId",
    total_cpu_quota, total_memory_quota, total_disk_quota,
    max_cpu_per_sandbox, max_memory_per_sandbox, max_disk_per_sandbox,
    max_disk_per_non_ephemeral_sandbox
  )
  VALUES (org_id, 'us', 4, 7, 100, 2, 4, 20, 20)
  ON CONFLICT ("organizationId", "regionId") DO UPDATE SET
    total_cpu_quota = EXCLUDED.total_cpu_quota,
    total_memory_quota = EXCLUDED.total_memory_quota,
    total_disk_quota = EXCLUDED.total_disk_quota,
    max_cpu_per_sandbox = EXCLUDED.max_cpu_per_sandbox,
    max_memory_per_sandbox = EXCLUDED.max_memory_per_sandbox,
    max_disk_per_sandbox = EXCLUDED.max_disk_per_sandbox,
    max_disk_per_non_ephemeral_sandbox = EXCLUDED.max_disk_per_non_ephemeral_sandbox,
    "updatedAt" = NOW();
END
$$;
SQL
`, keyHash, keyPrefix, keySuffix, name)
	_, err := ssh.Run(script)
	return err
}

func insertAPIKeyLocal(name, key string) error {
	hash := sha256.Sum256([]byte(key))
	keyHash := hex.EncodeToString(hash[:])
	keyPrefix := key[:8]
	keySuffix := key[len(key)-4:]

	sql := fmt.Sprintf(`DO $$ DECLARE org_id uuid; user_id text; BEGIN `+
		`SELECT id INTO org_id FROM organization LIMIT 1; `+
		`SELECT id INTO user_id FROM "user" LIMIT 1; `+
		`INSERT INTO api_key ("keyHash", "keyPrefix", "keySuffix", name, "createdAt", "organizationId", "userId", permissions) `+
		`VALUES ('%s', '%s', '%s', '%s', NOW(), org_id, user_id, `+
		`'{write:sandboxes,delete:sandboxes,write:snapshots,delete:snapshots,read:volumes,write:volumes,delete:volumes,read:runners,write:runners,read:audit_logs}'::api_key_permissions_enum[]) `+
		`ON CONFLICT ("keyHash") DO NOTHING; `+
		`UPDATE organization SET max_cpu_per_sandbox = 2, max_memory_per_sandbox = 4, max_disk_per_sandbox = 20, volume_quota = 100, "defaultRegionId" = COALESCE("defaultRegionId", 'us'), "updatedAt" = NOW() WHERE id = org_id; `+
		`INSERT INTO region_quota ("organizationId", "regionId", total_cpu_quota, total_memory_quota, total_disk_quota, max_cpu_per_sandbox, max_memory_per_sandbox, max_disk_per_sandbox, max_disk_per_non_ephemeral_sandbox) `+
		`VALUES (org_id, 'us', 4, 7, 100, 2, 4, 20, 20) `+
		`ON CONFLICT ("organizationId", "regionId") DO UPDATE SET total_cpu_quota = EXCLUDED.total_cpu_quota, total_memory_quota = EXCLUDED.total_memory_quota, total_disk_quota = EXCLUDED.total_disk_quota, max_cpu_per_sandbox = EXCLUDED.max_cpu_per_sandbox, max_memory_per_sandbox = EXCLUDED.max_memory_per_sandbox, max_disk_per_sandbox = EXCLUDED.max_disk_per_sandbox, max_disk_per_non_ephemeral_sandbox = EXCLUDED.max_disk_per_non_ephemeral_sandbox, "updatedAt" = NOW(); `+
		`END $$;`,
		keyHash, keyPrefix, keySuffix, name)

	// Write SQL to temp file to avoid shell escaping issues
	tmpFile := "/tmp/mob-insert-key.sql"
	if err := os.WriteFile(tmpFile, []byte(sql), 0600); err != nil {
		return err
	}
	defer os.Remove(tmpFile)

	_, err := localRun(fmt.Sprintf("docker exec -i daytona-db psql -U daytona -d daytona < %s", tmpFile))
	return err
}
