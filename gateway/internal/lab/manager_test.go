package lab

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	mu      sync.Mutex
	started []string
	stopped []string
	fail    bool
	block   chan struct{} // if set, Start waits on it
}

func (f *fakeRunner) Start(ctx context.Context, name string, env []string) (string, error) {
	f.mu.Lock()
	f.started = append(f.started, name)
	block, fail := f.block, f.fail
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if fail {
		return "", errors.New("boom")
	}
	return "10.0.0.2:8765", nil
}

func (f *fakeRunner) Stop(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, name)
	return nil
}

func (f *fakeRunner) StopAll(context.Context) error { return nil }

func probeOK(context.Context, string) ([]ActionInfo, error) {
	return []ActionInfo{{Name: "insert_rows"}}, nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestManager(r Runner, cfg Config) (*Manager, *clock) {
	if cfg.MaxLabs == 0 {
		cfg.MaxLabs = 2
	}
	if cfg.MaxQueue == 0 {
		cfg.MaxQueue = 10
	}
	if cfg.StartsPerHour == 0 {
		cfg.StartsPerHour = 5
	}
	cfg.Idle, cfg.Lifetime, cfg.QueueStale, cfg.StartTimeout = 15*time.Minute, 60*time.Minute, 2*time.Minute, time.Minute
	m := NewManager(cfg, r, probeOK, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	m.now = c.now
	return m, c
}

// settle waits for background launches and stops to finish.
func settle(m *Manager) { m.wg.Wait() }

func TestOneLabPerReader(t *testing.T) {
	r := &fakeRunner{}
	m, _ := newTestManager(r, Config{})
	m.Start("github:1")
	settle(m)
	st, _ := m.Start("github:1")
	settle(m)
	if st.State != Ready || len(r.started) != 1 {
		t.Fatalf("second Start: state %s, %d starts; want ready and 1", st.State, len(r.started))
	}
	if addr, actions, ok := m.Ready("github:1"); !ok || addr != "10.0.0.2:8765" || actions[0].Name != "insert_rows" {
		t.Fatalf("Ready = %q %v %v", addr, actions, ok)
	}
	// Same person on another provider is another reader.
	if st := m.Status("google:1"); st.State != None {
		t.Fatalf("google:1 state %s, want none", st.State)
	}
}

func TestCapQueuesAndPromotes(t *testing.T) {
	r := &fakeRunner{}
	m, _ := newTestManager(r, Config{MaxLabs: 2})
	for _, u := range []string{"a", "b", "c", "d"} {
		m.Start(u)
	}
	settle(m)
	if st := m.Status("c"); st.State != Queued || st.Position != 1 {
		t.Fatalf("c = %+v, want queued at 1", st)
	}
	if st := m.Status("d"); st.Position != 2 {
		t.Fatalf("d position %d, want 2", st.Position)
	}
	m.Stop("a")
	settle(m)
	if st := m.Status("c"); st.State != Ready {
		t.Fatalf("c after a stopped = %s, want ready", st.State)
	}
	if st := m.Status("d"); st.Position != 1 {
		t.Fatalf("d position %d, want 1", st.Position)
	}
	if len(r.stopped) != 1 {
		t.Fatalf("stopped %v, want a's lab only", r.stopped)
	}
}

func TestQueueFull(t *testing.T) {
	m, _ := newTestManager(&fakeRunner{}, Config{MaxLabs: 1, MaxQueue: 1})
	m.Start("a")
	m.Start("b")
	if _, err := m.Start("c"); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("err = %v, want ErrQueueFull", err)
	}
	settle(m)
}

func TestIdleReapCountsActionsOnly(t *testing.T) {
	r := &fakeRunner{}
	m, c := newTestManager(r, Config{})
	m.Start("a")
	settle(m)
	c.t = c.t.Add(10 * time.Minute)
	m.Touch("a") // an action
	c.t = c.t.Add(14 * time.Minute)
	m.Status("a") // polling a ready lab is not activity
	m.Reap()
	if st := m.Status("a"); st.State != Ready {
		t.Fatalf("after 14m since action: %s, want ready", st.State)
	}
	c.t = c.t.Add(time.Minute)
	m.Reap()
	settle(m)
	if st := m.Status("a"); st.State != None || len(r.stopped) != 1 {
		t.Fatalf("after 15m idle: %s, stopped %v", st.State, r.stopped)
	}
}

func TestLifetimeCap(t *testing.T) {
	m, c := newTestManager(&fakeRunner{}, Config{})
	m.Start("a")
	settle(m)
	for i := 0; i < 6; i++ {
		c.t = c.t.Add(10 * time.Minute)
		m.Touch("a")
		m.Reap()
	}
	settle(m)
	if st := m.Status("a"); st.State != None {
		t.Fatalf("after 60m of activity: %s, want none", st.State)
	}
}

func TestStaleQueueEntryDropped(t *testing.T) {
	m, c := newTestManager(&fakeRunner{}, Config{MaxLabs: 1})
	m.Start("a")
	m.Start("b")
	settle(m)
	c.t = c.t.Add(3 * time.Minute)
	m.Touch("a")
	m.Reap()
	if st := m.Status("b"); st.State != None {
		t.Fatalf("b stopped polling 3m ago: %s, want none", st.State)
	}
}

func TestStartsPerHour(t *testing.T) {
	m, c := newTestManager(&fakeRunner{}, Config{StartsPerHour: 2})
	for i := 0; i < 2; i++ {
		if _, err := m.Start("a"); err != nil {
			t.Fatal(err)
		}
		settle(m)
		m.Stop("a")
		settle(m)
	}
	if _, err := m.Start("a"); !errors.Is(err, ErrTooManyStarts) {
		t.Fatalf("third start: %v, want ErrTooManyStarts", err)
	}
	c.t = c.t.Add(61 * time.Minute)
	if _, err := m.Start("a"); err != nil {
		t.Fatalf("after an hour: %v", err)
	}
	settle(m)
}

func TestFailedStartFreesSlotAndCleansUp(t *testing.T) {
	r := &fakeRunner{fail: true}
	m, _ := newTestManager(r, Config{MaxLabs: 1})
	m.Start("a")
	m.Start("b")
	settle(m)
	if st := m.Status("a"); st.State != Failed {
		t.Fatalf("a = %s, want failed", st.State)
	}
	if len(r.stopped) < 1 {
		t.Fatal("failed lab was not cleaned up")
	}
	if st := m.Status("b"); st.State == Queued {
		t.Fatal("b still queued after a's slot was freed")
	}
}

func TestStopWhileStarting(t *testing.T) {
	r := &fakeRunner{block: make(chan struct{})}
	m, _ := newTestManager(r, Config{})
	m.Start("a")
	m.Stop("a") // cancels the start
	settle(m)
	if st := m.Status("a"); st.State != None {
		t.Fatalf("a = %s, want none", st.State)
	}
	if len(r.stopped) != 1 {
		t.Fatalf("stopped %v, want the half-started lab removed", r.stopped)
	}
}

func TestLabNameGuard(t *testing.T) {
	c := ComposeRunner{Dir: t.TempDir()}
	for _, bad := range []string{"lab-; rm -rf /", "../x", "lab-ABCDEF123456", ""} {
		if err := c.Stop(context.Background(), bad); err == nil || err.Error() != `refusing lab name "`+bad+`"` {
			t.Errorf("Stop(%q) = %v, want refusal", bad, err)
		}
	}
	if !labName.MatchString(newName()) {
		t.Fatal("generated names must pass the guard")
	}
}
