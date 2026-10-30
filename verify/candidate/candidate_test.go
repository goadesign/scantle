package candidate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probeResult mirrors the probe's output line.
type probeResult struct {
	Action  string `json:"action"`
	Allowed bool   `json:"allowed"`
	Detail  string `json:"detail"`
}

// TestExecEnforcesTheSandbox runs a hostile probe binary through Exec and
// checks, from what the operating system actually allowed, that every
// forbidden access fails and every assigned access works.
func TestExecEnforcesTheSandbox(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the sandbox is implemented with sandbox-exec on macOS")
	}
	probe := buildProbe(t)
	state := t.TempDir()
	protected := t.TempDir()
	secret := filepath.Join(protected, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("evaluator secret"), 0o600))
	allowed := listen(t)
	unassigned := listen(t)
	listenPort := freePort(t)
	t.Setenv("EVAL_SECRET", "must not leak")

	p, err := Exec(t.Context(), probe, Spec{
		Args: []string{
			"read:" + secret,
			"write:" + filepath.Join(protected, "out.txt"),
			"write:" + filepath.Join(state, "ok.txt"),
			"env:EVAL_SECRET",
			"env:GIVEN",
			fmt.Sprintf("connect:127.0.0.1:%d", allowed),
			fmt.Sprintf("connect:127.0.0.1:%d", unassigned),
			"connect:1.1.1.1:53",
			fmt.Sprintf("listen:127.0.0.1:%d", listenPort),
			fmt.Sprintf("listen:127.0.0.1:%d", freePort(t)),
			"spawn",
			"read:/etc/hosts",
		},
		Env:          []string{"GIVEN=explicit"},
		StateDir:     state,
		ConnectPorts: []int{allowed},
		ListenPort:   listenPort,
	})
	require.NoError(t, err)
	require.NoError(t, p.Wait(), p.Output())

	got := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(p.Output()))
	for sc.Scan() {
		var r probeResult
		require.NoError(t, json.Unmarshal(sc.Bytes(), &r), sc.Text())
		got[strings.SplitN(r.Action, ":", 2)[0]+"@"+r.Action] = r.Allowed
	}
	cases := []struct {
		name   string
		action string
		allow  bool
	}{
		{"read a protected file", "read:" + secret, false},
		{"write outside its state", "write:" + filepath.Join(protected, "out.txt"), false},
		{"write its own state", "write:" + filepath.Join(state, "ok.txt"), true},
		{"see the evaluator's environment", "env:EVAL_SECRET", false},
		{"see its explicit environment", "env:GIVEN", true},
		{"connect to an assigned port", fmt.Sprintf("connect:127.0.0.1:%d", allowed), true},
		{"connect to an unassigned loopback port", fmt.Sprintf("connect:127.0.0.1:%d", unassigned), false},
		{"connect off the machine", "connect:1.1.1.1:53", false},
		{"listen on its assigned port", fmt.Sprintf("listen:127.0.0.1:%d", listenPort), true},
		{"spawn a child process", "spawn", false},
		{"read other system files", "read:/etc/hosts", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, ok := got[strings.SplitN(c.action, ":", 2)[0]+"@"+c.action]
			require.True(t, ok, "probe did not report %s", c.action)
			assert.Equal(t, c.allow, v)
		})
	}
	listenOthers := 0
	for k, v := range got {
		if strings.HasPrefix(k, "listen@") && !strings.HasSuffix(k, fmt.Sprintf(":%d", listenPort)) {
			listenOthers++
			assert.False(t, v, "listened on an unassigned port: %s", k)
		}
	}
	assert.Equal(t, 1, listenOthers)
}

func TestExecRefusesAModifiedBinary(t *testing.T) {
	if runtime.GOOS != "darwin" {
		_, err := Exec(t.Context(), Artifact{}, Spec{})
		require.ErrorIs(t, err, ErrIsolationUnavailable)
		return
	}
	probe := buildProbe(t)
	probe.Digest = strings.Repeat("0", 64)
	_, err := Exec(t.Context(), probe, Spec{StateDir: t.TempDir()})
	require.ErrorIs(t, err, ErrDigestMismatch)
}

// buildProbe builds the hostile probe and pins it.
func buildProbe(t *testing.T) Artifact {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "probe")
	build := exec.Command("go", "build", "-o", bin, "./testdata/probe")
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))
	a, err := NewArtifact(bin)
	require.NoError(t, err)
	return a
}

// listen opens a loopback listener for the test's lifetime and returns its port.
func listen(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// freePort returns a currently unused loopback port.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}
