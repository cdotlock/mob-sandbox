package guardian

import (
	"fmt"
	"log"
	"strings"
	"time"
)

type Guardian struct {
	sshRun       func(string) (string, error)
	checkInterval time.Duration
	failCount    int
}

func New(sshRun func(string) (string, error)) *Guardian {
	return &Guardian{
		sshRun:        sshRun,
		checkInterval: 30 * time.Second,
	}
}

func (g *Guardian) Run() {
	log.Println("guardian: starting health check loop")
	ticker := time.NewTicker(g.checkInterval)
	defer ticker.Stop()

	cleanupTicker := time.NewTicker(5 * time.Minute)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ticker.C:
			g.healthCheck()
		case <-cleanupTicker.C:
			g.cleanup()
		}
	}
}

func (g *Guardian) healthCheck() {
	containers := []string{
		"daytona-api", "daytona-runner", "daytona-proxy",
		"daytona-db", "daytona-redis", "daytona-registry",
		"daytona-minio", "daytona-ssh-gateway", "daytona-dex",
	}

	allHealthy := true
	for _, c := range containers {
		out, err := g.sshRun(fmt.Sprintf("docker inspect --format='{{.State.Running}}' %s 2>/dev/null || echo false", c))
		if err != nil || strings.TrimSpace(out) != "true" {
			log.Printf("guardian: %s is down", c)
			allHealthy = false
		}
	}

	if !allHealthy {
		g.failCount++
		if g.failCount >= 3 {
			log.Println("guardian: 3 consecutive failures, restarting stack")
			g.restartStack()
			g.failCount = 0
		}
	} else {
		g.failCount = 0
	}

	g.ensureToolbox()
	g.ensureRegistryHosts()
}

func (g *Guardian) restartStack() {
	g.sshRun("cd /opt/mob-sandbox && docker compose -f docker-compose.daytona.yml up -d")
}

func (g *Guardian) ensureToolbox() {
	out, _ := g.sshRun("test -x /usr/local/bin/.tmp/binaries/daemon-amd64 && echo ok || echo missing")
	if strings.TrimSpace(out) == "missing" {
		log.Println("guardian: toolbox binary missing, extracting")
		script := `
BINDIR=/usr/local/bin/.tmp/binaries
mkdir -p "$BINDIR"
docker exec daytona-runner cat /usr/local/bin/.tmp/binaries/daemon-amd64 > "$BINDIR/daemon-amd64.tmp"
mv "$BINDIR/daemon-amd64.tmp" "$BINDIR/daemon-amd64"
chmod +x "$BINDIR/daemon-amd64"
`
		g.sshRun(script)
	}
}

func (g *Guardian) ensureRegistryHosts() {
	out, _ := g.sshRun("grep -c '[[:space:]]registry$' /etc/hosts")
	if strings.TrimSpace(out) == "0" {
		log.Println("guardian: registry /etc/hosts entry missing, fixing")
		script := `
REGISTRY_IP=$(docker inspect daytona-registry --format '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' 2>/dev/null | awk '{print $1}')
if [ -n "$REGISTRY_IP" ]; then
  sed -i '/[[:space:]]registry$/d' /etc/hosts
  echo "$REGISTRY_IP registry" >> /etc/hosts
fi
`
		g.sshRun(script)
	}
}

func (g *Guardian) cleanup() {
	// Clean error-state sandboxes older than 1 hour
	g.sshRun(`
docker ps -a --filter "label=daytona.sandbox" --filter "status=exited" --format "{{.ID}} {{.CreatedAt}}" | while read id created; do
  age=$(( $(date +%s) - $(date -d "$created" +%s 2>/dev/null || echo 0) ))
  if [ "$age" -gt 3600 ]; then
    docker rm -f "$id" 2>/dev/null
  fi
done
`)

	// Clean orphan OpenHands agent containers
	g.sshRun(`
docker ps --filter "name=oh-agent-server" --format "{{.Names}} {{.Status}}" | while read name status; do
  hours=$(echo "$status" | grep -oP '\d+(?= hours)' || echo 0)
  if [ "$hours" -gt 6 ]; then
    docker rm -f "$name" 2>/dev/null
  fi
done
`)
}
