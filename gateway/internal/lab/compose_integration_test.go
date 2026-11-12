package lab

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestComposeRunnerAgainstDocker starts and removes a real lab. It needs a
// Docker daemon whose container IPs this process can reach (the gateway
// shares dind's network namespace in production), the lab images, and
// GLASSHOUSE_LAB_DIR holding compose.yml + lab.override.yml:
//
//	make gateway-integration
func TestComposeRunnerAgainstDocker(t *testing.T) {
	dir := os.Getenv("GLASSHOUSE_LAB_DIR")
	if dir == "" {
		t.Skip("set GLASSHOUSE_LAB_DIR to run against Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	r := ComposeRunner{Dir: dir, Env: DockerEnv()}
	name := newName()
	addr, err := r.Start(ctx, name, []string{"GLASSHOUSE_PG_ADMIN_PASSWORD=a" + randomHex(8), "GLASSHOUSE_PG_INSPECTOR_PASSWORD=i" + randomHex(8)})
	t.Cleanup(func() { _ = r.StopAll(context.Background()) })
	if err != nil {
		t.Fatal(err)
	}
	actions, err := HTTPProbe(&http.Client{Timeout: 3 * time.Second})(ctx, addr)
	if err != nil || len(actions) == 0 {
		t.Fatalf("probe: %v %v", actions, err)
	}

	// The lab has no egress: its network is internal.
	out, err := r.docker(ctx, nil, "exec", name+"-postgres-1", "sh", "-c", "wget -q -T 3 -O /dev/null http://1.1.1.1 && echo open || echo blocked")
	if err != nil || strings.TrimSpace(string(out)) != "blocked" {
		t.Fatalf("egress check: %q %v", out, err)
	}
	// No published ports.
	out, _ = r.docker(ctx, nil, "ps", "--filter", "label=com.docker.compose.project="+name, "--format", "{{.Ports}}")
	if strings.Contains(string(out), "->") {
		t.Fatalf("lab publishes ports: %q", out)
	}

	if err := r.Stop(ctx, name); err != nil {
		t.Fatal(err)
	}
	out, _ = r.docker(ctx, nil, "ps", "-aq", "--filter", "label="+Label)
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("containers left after Stop: %q", out)
	}
}
