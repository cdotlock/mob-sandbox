package control

import "testing"

func TestValidateExposeRequestGuardianFields(t *testing.T) {
	valid := ExposeRequest{
		SandboxID:    "ca4cfdc5-a605-40be-ac1a-dc0df4fbe9f8",
		Port:         8888,
		Name:         "moonshort",
		StartCommand: "cd /app; nohup python3 -m uvicorn api:app --host 0.0.0.0 --port 8888 > app.log 2>&1 &",
		HealthPath:   "/health",
	}
	if err := validateExposeRequest(valid); err != nil {
		t.Fatalf("valid request failed: %v", err)
	}

	invalidPath := valid
	invalidPath.HealthPath = "health"
	if err := validateExposeRequest(invalidPath); err == nil {
		t.Fatal("expected health_path without leading slash to fail")
	}

	invalidCommand := valid
	invalidCommand.StartCommand = "echo bad\x00"
	if err := validateExposeRequest(invalidCommand); err == nil {
		t.Fatal("expected start_command with NUL byte to fail")
	}
}

func TestPublicURLModes(t *testing.T) {
	ipServer := NewServerWithOptions(Options{
		Mode:     "ip",
		PublicIP: "47.254.93.15",
		Port:     9876,
	})
	if got, want := ipServer.publicURL("moonshort"), "http://moonshort.47.254.93.15.sslip.io:9876"; got != want {
		t.Fatalf("ip publicURL = %q, want %q", got, want)
	}

	domainServer := NewServerWithOptions(Options{
		Mode:   "domain",
		Domain: "example.com",
	})
	if got, want := domainServer.publicURL("moonshort"), "https://moonshort.example.com"; got != want {
		t.Fatalf("domain publicURL = %q, want %q", got, want)
	}
}
