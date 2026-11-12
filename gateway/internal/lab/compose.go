package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Label marks every container, network and volume of every lab.
const Label = "glasshouse.lab=1"

var labName = regexp.MustCompile(`^lab-[0-9a-f]{12}$`)

// ComposeRunner starts labs with `docker compose` against the lab Docker
// daemon (dind in production), from the unchanged
// adapters/postgres/compose.yml plus lab.override.yml.
type ComposeRunner struct {
	Dir string // holds compose.yml and lab.override.yml
	// Env is the only environment docker sees (DOCKER_HOST, PATH, ...): the
	// gateway's own secrets are never passed to a child process.
	Env []string
}

func (c ComposeRunner) compose(ctx context.Context, name string, extraEnv []string, args ...string) ([]byte, error) {
	if !labName.MatchString(name) {
		return nil, fmt.Errorf("refusing lab name %q", name)
	}
	full := append([]string{"compose", "-p", name,
		"-f", filepath.Join(c.Dir, "compose.yml"),
		"-f", filepath.Join(c.Dir, "lab.override.yml")}, args...)
	return c.docker(ctx, extraEnv, full...)
}

func (c ComposeRunner) docker(ctx context.Context, extraEnv []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = append(append(append([]string{}, c.Env...), labEnv...), extraEnv...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// labEnv fills the variables compose.yml interpolates but a lab does not use:
// the Inspector is a prebuilt image, and the gateway sends no Origin.
var labEnv = []string{
	"GLASSHOUSE_INSPECTOR_CONTEXT=unused-prebuilt-image",
	"GLASSHOUSE_ALLOWED_ORIGIN=http://localhost",
}

func (c ComposeRunner) Start(ctx context.Context, name string, env []string) (string, error) {
	if _, err := c.compose(ctx, name, env, "up", "-d", "--wait", "--no-build", "--pull", "never"); err != nil {
		return "", err
	}
	out, err := c.docker(ctx, nil, "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", name+"-inspector-1")
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(strings.TrimSpace(string(out)))
	if ip == nil {
		return "", fmt.Errorf("lab %s: no Inspector IP in %q", name, out)
	}
	return net.JoinHostPort(ip.String(), "8765"), nil
}

func (c ComposeRunner) Stop(ctx context.Context, name string) error {
	_, err := c.compose(ctx, name, nil, "down", "-v", "--remove-orphans", "--timeout", "2")
	return err
}

func (c ComposeRunner) StopAll(ctx context.Context) error {
	out, err := c.docker(ctx, nil, "ps", "-aq", "--filter", "label="+Label)
	if err != nil {
		return err
	}
	if ids := strings.Fields(string(out)); len(ids) > 0 {
		if _, err := c.docker(ctx, nil, append([]string{"rm", "-f", "-v"}, ids...)...); err != nil {
			return err
		}
	}
	if _, err := c.docker(ctx, nil, "network", "prune", "-f", "--filter", "label="+Label); err != nil {
		return err
	}
	_, err = c.docker(ctx, nil, "volume", "prune", "-f", "-a", "--filter", "label="+Label)
	return err
}

// DockerEnv returns the minimal environment for docker: what it needs to
// find the daemon and its config, nothing else from the gateway.
func DockerEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONFIG", "DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY",
		"GLASSHOUSE_INSPECTOR_IMAGE", "GLASSHOUSE_POSTGRES_IMAGE"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// InspectorHost is the Host header the gateway sends: the Inspector's own
// allowlist accepts localhost:<port>, and the gateway sends no Origin, so it
// passes the Inspector's checks without any Inspector change.
const InspectorHost = "localhost:8765"

// HTTPProbe polls the Inspector's /health until it answers, and returns its
// action list.
func HTTPProbe(client *http.Client) Prober {
	return func(ctx context.Context, addr string) ([]ActionInfo, error) {
		for {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/health", nil)
			if err != nil {
				return nil, err
			}
			req.Host = InspectorHost
			if resp, err := client.Do(req); err == nil {
				var h struct {
					Actions []ActionInfo `json:"actions"`
				}
				decErr := json.NewDecoder(resp.Body).Decode(&h)
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK && decErr == nil {
					return h.Actions, nil
				}
			}
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("inspector at %s never became healthy: %w", addr, ctx.Err())
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
}
