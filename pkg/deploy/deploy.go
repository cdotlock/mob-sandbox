package deploy

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/cdotlock/mob-sandbox/pkg/config"
	"github.com/cdotlock/mob-sandbox/pkg/dns"
	"github.com/cdotlock/mob-sandbox/pkg/embedded"
	"github.com/cdotlock/mob-sandbox/pkg/ui"
)

const totalSteps = 20

type Deployer struct {
	cfg       *config.ServerConfig
	dns       dns.Provider
	sshRun    func(cmd string) (string, error)
	sshUpload func(content []byte, path string) error
	secrets   *secrets
}

type secrets struct {
	DBPassword        string
	RegistryPassword  string
	MinioPassword     string
	EncryptionKey     string
	ProxyAPIKey       string
	RunnerAPIKey      string
	SSHGatewayAPIKey  string
	GWPrivateKeyB64   string
	GWPublicKeyB64    string
	HostKeyB64        string
	AdminPasswordHash string
	DaytonaAPIKey     string
	DaytonaAPIKeyHash string
}

func New(cfg *config.ServerConfig, sshRun func(string) (string, error), sshUpload func([]byte, string) error) *Deployer {
	return &Deployer{cfg: cfg, sshRun: sshRun, sshUpload: sshUpload}
}

func (d *Deployer) Run() error {
	d.secrets = &secrets{
		DBPassword:        randomHex(16),
		RegistryPassword:  randomHex(12),
		MinioPassword:     randomHex(16),
		EncryptionKey:     randomHex(16),
		ProxyAPIKey:       randomHex(16),
		RunnerAPIKey:      randomHex(16),
		SSHGatewayAPIKey:  randomHex(16),
		AdminPasswordHash: "$2a$10$2b2cU8CPhOTaGrs1HRQuAueS7JTT5ZHsHSzYiFPm1leZck7Mc8T4W",
	}

	steps := []struct {
		name string
		fn   func() error
	}{
		{"Detect system", d.step01DetectSystem},
		{"Install Docker CE", d.step02InstallDocker},
		{"Configure Docker daemon", d.step03ConfigureDocker},
		{"Configure UFW firewall", d.step04ConfigureUFW},
		{"Write embedded files", d.step05WriteEmbeddedFiles},
		{"Generate SSH keypairs", d.step06GenerateSSHKeys},
		{"Generate API key", d.step07GenerateAPIKey},
		{"Detect public IP", d.step08DetectPublicIP},
		{"Configure DNS", d.step09ConfigureDNS},
		{"Generate compose files", d.step10GenerateCompose},
		{"Deploy Traefik", d.step11DeployTraefik},
		{"Start Daytona stack", d.step12StartDaytona},
		{"Wait for Daytona API health", d.step13WaitDaytonaHealth},
		{"Extract toolbox binary", d.step14ExtractToolbox},
		{"Configure registry /etc/hosts", d.step15ConfigureRegistry},
		{"Insert API key into DB", d.step16InsertAPIKey},
		{"Build sandbox image", d.step17BuildSandboxImage},
		{"Register snapshot", d.step18RegisterSnapshot},
		{"Deploy OpenHands", d.step19DeployOpenHands},
		{"Register systemd service", d.step20RegisterSystemd},
	}

	for i, step := range steps {
		sp := ui.Step(i+1, totalSteps, step.name)
		if err := step.fn(); err != nil {
			ui.StepFail(sp, fmt.Sprintf("%s: %v", step.name, err))
			return fmt.Errorf("step %d (%s): %w", i+1, step.name, err)
		}
		ui.StepDone(sp, step.name)
	}
	return nil
}

func (d *Deployer) step01DetectSystem() error {
	out, err := d.sshRun("uname -a && free -h | head -2 && df -h / | tail -1")
	if err != nil {
		return fmt.Errorf("system check: %w\n%s", err, out)
	}
	return nil
}

func (d *Deployer) step02InstallDocker() error {
	script := `
if command -v docker &>/dev/null; then
  echo "Docker already installed: $(docker --version)"
  exit 0
fi
curl -fsSL https://get.docker.com | sh
systemctl enable docker
systemctl start docker
docker --version
`
	out, err := d.sshRun(script)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

func (d *Deployer) step03ConfigureDocker() error {
	script := `
mkdir -p /etc/docker
cat > /etc/docker/daemon.json <<'JSON'
{
  "insecure-registries": ["registry:6000"]
}
JSON
systemctl reload docker || systemctl restart docker
sleep 2
`
	_, err := d.sshRun(script)
	return err
}

func (d *Deployer) step04ConfigureUFW() error {
	script := fmt.Sprintf(`
if ! command -v ufw &>/dev/null; then
  apt-get install -y ufw
fi
ufw allow 22/tcp
ufw allow 80/tcp
ufw allow 443/tcp
ufw allow %d/tcp
ufw allow %d/tcp
ufw --force enable
ufw status
`, d.cfg.Ports.SSH, d.cfg.Ports.Control)
	_, err := d.sshRun(script)
	return err
}

func (d *Deployer) step05WriteEmbeddedFiles() error {
	dir := d.cfg.InstallDir
	d.sshRun(fmt.Sprintf("mkdir -p %s", dir))

	dockerfile, err := embedded.FS.ReadFile("Dockerfile.sandbox")
	if err != nil {
		return err
	}
	return d.sshUpload(dockerfile, dir+"/Dockerfile.sandbox")
}

func (d *Deployer) step06GenerateSSHKeys() error {
	script := `
ssh-keygen -t rsa -b 4096 -f /tmp/daytona-gateway -N "" -C "daytona-gateway" -q 2>/dev/null || true
ssh-keygen -t rsa -b 4096 -f /tmp/daytona-host    -N "" -C "daytona-host"    -q 2>/dev/null || true
echo "GW_PRIV:$(base64 -w0 /tmp/daytona-gateway)"
echo "GW_PUB:$(base64 -w0 /tmp/daytona-gateway.pub)"
echo "HOST_KEY:$(base64 -w0 /tmp/daytona-host)"
rm -f /tmp/daytona-gateway /tmp/daytona-gateway.pub /tmp/daytona-host /tmp/daytona-host.pub
`
	out, err := d.sshRun(script)
	if err != nil {
		return err
	}

	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "GW_PRIV:") {
			d.secrets.GWPrivateKeyB64 = strings.TrimPrefix(line, "GW_PRIV:")
		} else if strings.HasPrefix(line, "GW_PUB:") {
			d.secrets.GWPublicKeyB64 = strings.TrimPrefix(line, "GW_PUB:")
		} else if strings.HasPrefix(line, "HOST_KEY:") {
			d.secrets.HostKeyB64 = strings.TrimPrefix(line, "HOST_KEY:")
		}
	}

	if d.secrets.GWPrivateKeyB64 == "" || d.secrets.GWPublicKeyB64 == "" {
		return fmt.Errorf("failed to extract SSH keys from output")
	}
	return nil
}

func (d *Deployer) step07GenerateAPIKey() error {
	if d.cfg.APIKey != "" {
		d.secrets.DaytonaAPIKey = d.cfg.APIKey
	} else {
		d.secrets.DaytonaAPIKey = "mob-" + randomHex(20)
	}
	hash := sha256.Sum256([]byte(d.secrets.DaytonaAPIKey))
	d.secrets.DaytonaAPIKeyHash = hex.EncodeToString(hash[:])
	d.cfg.APIKey = d.secrets.DaytonaAPIKey
	return nil
}

func (d *Deployer) step08DetectPublicIP() error {
	if d.cfg.PublicIP != "" {
		return nil
	}
	out, err := d.sshRun("curl -sf https://api.ipify.org || curl -sf https://ifconfig.me")
	if err != nil {
		return fmt.Errorf("detect IP: %w", err)
	}
	d.cfg.PublicIP = strings.TrimSpace(out)
	return nil
}

func (d *Deployer) step09ConfigureDNS() error {
	if d.cfg.Mode != "domain" {
		return nil
	}

	var err error
	d.dns, err = dns.NewProvider(d.cfg.DNS.Provider, d.cfg.DNS.Token, d.cfg.DNS.Secret)
	if err != nil {
		return err
	}
	return d.dns.EnsureRecords(d.cfg.Domain, d.cfg.PublicIP)
}

func (d *Deployer) step10GenerateCompose() error {
	dir := d.cfg.InstallDir

	apiBaseURL := fmt.Sprintf("http://%s:%d", d.cfg.PublicIP, d.cfg.Ports.API)
	proxyDomain := fmt.Sprintf("%s:%d", d.cfg.PublicIP, d.cfg.Ports.Proxy)
	proxyProtocol := "http"
	domain := d.cfg.PublicIP

	if d.cfg.Mode == "domain" {
		apiBaseURL = fmt.Sprintf("https://daytona.%s", d.cfg.Domain)
		proxyDomain = fmt.Sprintf("node.proxy.%s", d.cfg.Domain)
		proxyProtocol = "https"
		domain = d.cfg.Domain
	}

	daytonaData := map[string]string{
		"EncryptionKey":           d.secrets.EncryptionKey,
		"DBPassword":              d.secrets.DBPassword,
		"RegistryPassword":        d.secrets.RegistryPassword,
		"MinioPassword":           d.secrets.MinioPassword,
		"APIBaseURL":              apiBaseURL,
		"Domain":                  domain,
		"ProxyDomain":             proxyDomain,
		"ProxyProtocol":           proxyProtocol,
		"ProxyAPIKey":             d.secrets.ProxyAPIKey,
		"RunnerAPIKey":            d.secrets.RunnerAPIKey,
		"SSHGatewayAPIKey":        d.secrets.SSHGatewayAPIKey,
		"SSHGatewayPublicKeyB64":  d.secrets.GWPublicKeyB64,
		"SSHGatewayPrivateKeyB64": d.secrets.GWPrivateKeyB64,
		"SSHHostKeyB64":           d.secrets.HostKeyB64,
		"PublicIP":                d.cfg.PublicIP,
		"SSHPort":                 fmt.Sprintf("%d", d.cfg.Ports.SSH),
		"InstallDir":              dir,
	}

	daytonaYml, err := embedded.RenderTemplate("docker-compose.daytona.yml.tmpl", daytonaData)
	if err != nil {
		return fmt.Errorf("render daytona compose: %w", err)
	}
	if err := d.sshUpload(daytonaYml, dir+"/docker-compose.daytona.yml"); err != nil {
		return err
	}

	// Dex config
	dexData := map[string]string{
		"APIBaseURL":        apiBaseURL,
		"Domain":            domain,
		"AdminPasswordHash": d.secrets.AdminPasswordHash,
	}
	dexYml, err := embedded.RenderTemplate("dex-config.yaml.tmpl", dexData)
	if err != nil {
		return fmt.Errorf("render dex config: %w", err)
	}
	if err := d.sshUpload(dexYml, dir+"/dex-config.yaml"); err != nil {
		return err
	}

	// Traefik routes (domain mode only)
	if d.cfg.Mode == "domain" {
		routesData := map[string]string{
			"Domain":        d.cfg.Domain,
			"DomainEscaped": strings.ReplaceAll(d.cfg.Domain, ".", "\\."),
		}
		routesYml, err := embedded.RenderTemplate("traefik-routes.yml.tmpl", routesData)
		if err != nil {
			return fmt.Errorf("render traefik routes: %w", err)
		}
		d.sshRun("mkdir -p /etc/traefik/dynamic")
		if err := d.sshUpload(routesYml, "/etc/traefik/dynamic/routes.yml"); err != nil {
			return err
		}

		traefikData := map[string]string{
			"ACMEEmail":       "admin@" + d.cfg.Domain,
			"DNSProvider":     d.dns.Name(),
			"TraefikEnvBlock": d.dns.TraefikEnvBlock(),
		}
		traefikYml, err := embedded.RenderTemplate("docker-compose.traefik.yml.tmpl", traefikData)
		if err != nil {
			return fmt.Errorf("render traefik compose: %w", err)
		}
		if err := d.sshUpload(traefikYml, dir+"/docker-compose.traefik.yml"); err != nil {
			return err
		}
	}

	return nil
}

func (d *Deployer) step11DeployTraefik() error {
	if d.cfg.Mode != "domain" {
		return nil
	}
	dir := d.cfg.InstallDir
	script := fmt.Sprintf(`
docker network create edge 2>/dev/null || true
cd %s && docker compose -f docker-compose.traefik.yml up -d
`, dir)
	_, err := d.sshRun(script)
	return err
}

func (d *Deployer) step12StartDaytona() error {
	dir := d.cfg.InstallDir
	script := fmt.Sprintf(`
docker network create edge 2>/dev/null || true
docker network create daytona-network 2>/dev/null || true
docker network create runner-bridge --subnet 10.100.0.0/24 2>/dev/null || true
cd %s && docker compose -f docker-compose.daytona.yml up -d
`, dir)
	_, err := d.sshRun(script)
	return err
}

func (d *Deployer) step13WaitDaytonaHealth() error {
	for i := 0; i < 60; i++ {
		out, _ := d.sshRun(`docker inspect --format='{{.State.Health.Status}}' daytona-api 2>/dev/null || echo "missing"`)
		status := strings.TrimSpace(out)
		if status == "healthy" {
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("daytona-api never became healthy (5min timeout)")
}

func (d *Deployer) step14ExtractToolbox() error {
	script := `
BINDIR=/usr/local/bin/.tmp/binaries
mkdir -p "$BINDIR"
docker exec daytona-runner cat /usr/local/bin/.tmp/binaries/daemon-amd64 > "$BINDIR/daemon-amd64.tmp"
mv "$BINDIR/daemon-amd64.tmp" "$BINDIR/daemon-amd64"
chmod +x "$BINDIR/daemon-amd64"
docker exec daytona-runner cat /usr/local/bin/.tmp/binaries/daytona-computer-use > "$BINDIR/daytona-computer-use" 2>/dev/null || true
[ -f "$BINDIR/daytona-computer-use" ] && chmod +x "$BINDIR/daytona-computer-use" || true
ls -lh "$BINDIR/"
`
	_, err := d.sshRun(script)
	return err
}

func (d *Deployer) step15ConfigureRegistry() error {
	script := `
for i in $(seq 1 30); do
  if docker inspect daytona-registry &>/dev/null; then break; fi
  sleep 2
done
REGISTRY_IP=$(docker inspect daytona-registry --format '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' | awk '{print $1}')
sed -i '/[[:space:]]registry$/d' /etc/hosts
echo "$REGISTRY_IP registry" >> /etc/hosts
echo "Registry IP: $REGISTRY_IP"
`
	_, err := d.sshRun(script)
	return err
}

func (d *Deployer) step16InsertAPIKey() error {
	apiKey := d.secrets.DaytonaAPIKey
	keyHash := d.secrets.DaytonaAPIKeyHash
	keyPrefix := apiKey[:8]
	keySuffix := apiKey[len(apiKey)-4:]

	script := fmt.Sprintf(`
for i in $(seq 1 30); do
  if docker exec daytona-db psql -U daytona -c '\q' &>/dev/null; then break; fi
  sleep 3
done

for i in $(seq 1 30); do
  if docker exec daytona-db psql -U daytona -d daytona -c 'SELECT 1 FROM "user" LIMIT 1' &>/dev/null; then
    break
  fi
  sleep 5
done

docker exec daytona-db psql -U daytona -d daytona <<'SQL'
DO $$
DECLARE
  org_id uuid;
  user_id text;
BEGIN
  SELECT id INTO org_id FROM organization LIMIT 1;
  SELECT id INTO user_id FROM "user" LIMIT 1;

  IF org_id IS NULL OR user_id IS NULL THEN
    RAISE EXCEPTION 'org or user not found — wait for migrations';
  END IF;

  INSERT INTO api_key ("keyHash", "keyPrefix", "keySuffix", name, "createdAt", "organizationId", "userId", permissions)
  VALUES (
    '%s',
    '%s',
    '%s',
    'mob-admin',
    NOW(),
    org_id,
    user_id,
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
`, keyHash, keyPrefix, keySuffix)
	_, err := d.sshRun(script)
	return err
}

func (d *Deployer) step17BuildSandboxImage() error {
	dir := d.cfg.InstallDir
	script := fmt.Sprintf(`
cd %s
docker build -t mob-sandbox:1.0 -t registry:6000/mob-sandbox:1.0 -f Dockerfile.sandbox .
docker push registry:6000/mob-sandbox:1.0
`, dir)
	_, err := d.sshRun(script)
	return err
}

func (d *Deployer) step18RegisterSnapshot() error {
	apiURL := fmt.Sprintf("http://localhost:3000")
	if d.cfg.Mode == "domain" {
		apiURL = fmt.Sprintf("https://daytona.%s", d.cfg.Domain)
	}
	apiKey := d.secrets.DaytonaAPIKey

	script := fmt.Sprintf(`
SNAP=$(curl -sf -X POST \
  -H "Authorization: Bearer %s" \
  -H "Content-Type: application/json" \
  -d '{"name":"mob-sandbox:1.0","imageName":"registry:6000/mob-sandbox:1.0","entrypoint":["sleep","infinity"],"cpu":1,"memory":2,"disk":10}' \
  %s/api/snapshots 2>&1)
echo "Snapshot: $SNAP"

SNAP_ID=$(echo "$SNAP" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("id",""))' 2>/dev/null || true)
if [ -n "$SNAP_ID" ]; then
  for i in $(seq 1 60); do
    STATE=$(curl -sf -H "Authorization: Bearer %s" \
      %s/api/snapshots/$SNAP_ID \
      | python3 -c 'import sys,json; print(json.load(sys.stdin).get("state",""))' 2>/dev/null || echo "unknown")
    echo "  snapshot state: $STATE"
    [ "$STATE" = "active" ] && break
    [ "$STATE" = "error" ] && { echo "Snapshot error!" >&2; exit 1; }
    sleep 5
  done
fi
`, apiKey, apiURL, apiKey, apiURL)
	_, err := d.sshRun(script)
	return err
}

func (d *Deployer) step19DeployOpenHands() error {
	if d.cfg.LLM.APIKey == "" {
		ui.Warn("No LLM key provided, skipping OpenHands")
		return nil
	}

	dir := d.cfg.InstallDir
	if d.cfg.Mode == "domain" {
		return d.deployOpenHands(dir, fmt.Sprintf("https://openhands.%s", d.cfg.Domain), "")
	}

	startPort := d.cfg.Ports.OpenHands
	if startPort == 0 {
		startPort = config.DefaultOpenHandsPort
	}
	var lastErr error
	for i := 0; i < config.OpenHandsPortRetryLimit; i++ {
		port := startPort + i
		openHandsURL := fmt.Sprintf("http://%s:%d", d.cfg.PublicIP, port)
		err := d.deployOpenHands(dir, openHandsURL, strconv.Itoa(port))
		if err == nil {
			if port != startPort {
				ui.Warn("OpenHands port %d unavailable, using %d", startPort, port)
			}
			d.cfg.Ports.OpenHands = port
			d.allowOpenHandsPort(port)
			return nil
		}
		lastErr = err
		if !isPortConflictError(err) {
			return err
		}
		ui.Warn("OpenHands port %d is unavailable, retrying %d", port, port+1)
	}
	return fmt.Errorf("no available OpenHands port from %d to %d: %w", startPort, startPort+config.OpenHandsPortRetryLimit-1, lastErr)
}

func (d *Deployer) deployOpenHands(dir, openHandsURL, openHandsPort string) error {
	ohData := map[string]string{
		"LLMModel":      d.cfg.LLM.Model,
		"LLMBaseURL":    d.cfg.LLM.BaseURL,
		"LLMAPIKey":     d.cfg.LLM.APIKey,
		"OpenHandsURL":  openHandsURL,
		"OpenHandsPort": openHandsPort,
	}
	ohYml, err := embedded.RenderTemplate("docker-compose.openhands.yml.tmpl", ohData)
	if err != nil {
		return err
	}
	if err := d.sshUpload(ohYml, dir+"/docker-compose.openhands.yml"); err != nil {
		return err
	}

	script := fmt.Sprintf(`cd %s && docker compose -f docker-compose.openhands.yml up -d`, dir)
	out, err := d.sshRun(script)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

func (d *Deployer) allowOpenHandsPort(port int) {
	_, _ = d.sshRun(fmt.Sprintf("ufw allow %d/tcp >/dev/null 2>&1 || true", port))
}

func isPortConflictError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "address already in use") ||
		strings.Contains(msg, "port is already allocated") ||
		strings.Contains(msg, "bind:") ||
		strings.Contains(msg, "driver failed programming external connectivity")
}

func (d *Deployer) step20RegisterSystemd() error {
	dir := d.cfg.InstallDir

	serviceFile := fmt.Sprintf(`[Unit]
Description=mob-sandbox guardian daemon
After=docker.service network.target
Requires=docker.service

[Service]
Type=simple
ExecStart=/usr/local/bin/mob-server daemon
Restart=always
RestartSec=10
WorkingDirectory=%s

[Install]
WantedBy=multi-user.target
`, dir)

	if err := d.sshUpload([]byte(serviceFile), "/etc/systemd/system/mob-server.service"); err != nil {
		return err
	}

	script := `systemctl daemon-reload && systemctl enable mob-server.service`
	_, err := d.sshRun(script)
	return err
}

func (d *Deployer) GetAPIKey() string   { return d.secrets.DaytonaAPIKey }
func (d *Deployer) GetPublicIP() string { return d.cfg.PublicIP }

func RandomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func randomHex(n int) string {
	return RandomHex(n)
}

func DetectPublicIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr := conn.LocalAddr().(*net.UDPAddr)
	return addr.IP.String()
}
