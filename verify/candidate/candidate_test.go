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
			"stat:" + secret,
			"stat:" + filepath.Join(state, "ok.txt"),
		},
		Env:          []string{"GIVEN=explicit"},
		StateDir:     state,
		ConnectPorts: []int{allowed},
		ListenPort:   listenPort,
		LogFile:      filepath.Join(t.TempDir(), "probe.log"),
	})
	require.NoError(t, err)
	out, truncated, err := excerptAfterExit(p)
	require.NoError(t, err)
	require.False(t, truncated)

	got := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(out))
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
		{"learn whether a protected file exists", "stat:" + secret, false},
		{"stat its own state", "stat:" + filepath.Join(state, "ok.txt"), true},
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
	require.NoError(t, os.Chmod(filepath.Dir(probe.path), 0o700))
	require.NoError(t, os.Chmod(probe.path, 0o700))
	require.NoError(t, os.WriteFile(probe.path, []byte("tampered"), 0o700))
	_, err := Exec(t.Context(), probe, Spec{StateDir: t.TempDir(), LogFile: filepath.Join(t.TempDir(), "x.log")})
	require.ErrorIs(t, err, ErrDigestMismatch)
}

func TestPinIsolatesTheArtifactFromItsSource(t *testing.T) {
	src := filepath.Join(t.TempDir(), "bin")
	require.NoError(t, os.WriteFile(src, []byte("original"), 0o700))
	store := filepath.Join(t.TempDir(), "artifacts")
	t.Cleanup(func() { require.NoError(t, RemoveStore(store)) })
	a, err := Pin(store, src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(src, []byte("replaced after pinning"), 0o700))
	got, err := fileDigest(a.path)
	require.NoError(t, err)
	assert.Equal(t, a.Digest(), got, "the pinned copy keeps the original bytes")
	info, err := os.Stat(a.path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o500), info.Mode().Perm())
}

func TestExcerptIsBoundedAndReportsTruncation(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the sandbox is implemented with sandbox-exec on macOS")
	}
	probe := buildProbe(t)
	state := t.TempDir()
	p, err := Exec(t.Context(), probe, Spec{
		Args:     []string{"env:A", "env:B", "env:C"},
		StateDir: state,
		LogFile:  filepath.Join(t.TempDir(), "probe.log"),
	})
	require.NoError(t, err)
	require.NoError(t, p.Wait(), "the probe reports failed lookups in its output and exits normally")
	full, truncated, err := p.Excerpt(1 << 20)
	require.NoError(t, err)
	assert.False(t, truncated)
	tail, truncated, err := p.Excerpt(20)
	require.NoError(t, err)
	assert.True(t, truncated)
	assert.Len(t, tail, 20)
	assert.True(t, strings.HasSuffix(full, tail))
}

func TestExecRejectsALogFileInsideTheState(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the sandbox is implemented with sandbox-exec on macOS")
	}
	state := t.TempDir()
	_, err := Exec(t.Context(), buildProbe(t), Spec{StateDir: state, LogFile: filepath.Join(state, "probe.log")})
	require.ErrorContains(t, err, "outside the candidate state directory")
}

// excerptAfterExit waits for the process and returns its whole output.
func excerptAfterExit(p *Process) (string, bool, error) {
	if err := p.Wait(); err != nil {
		return "", false, err
	}
	return p.Excerpt(1 << 20)
}

// buildProbe builds the hostile probe and pins it.
func buildProbe(t *testing.T) Artifact {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "probe")
	build := exec.Command("go", "build", "-o", bin, "./testdata/probe")
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))
	store := filepath.Join(t.TempDir(), "artifacts")
	t.Cleanup(func() { require.NoError(t, RemoveStore(store)) })
	a, err := Pin(store, bin)
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

func TestBuildDeniesReadsOutsideTheModule(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the sandbox is implemented with sandbox-exec on macOS")
	}
	goroot, modCache := goEnv(t, "GOROOT"), goEnv(t, "GOMODCACHE")
	protected := t.TempDir()
	secret := filepath.Join(protected, "secret.h")
	require.NoError(t, os.WriteFile(secret, []byte("#define SECRET 42\n"), 0o600))

	mod := t.TempDir()
	includePath, err := filepath.Rel(mod, secret)
	require.NoError(t, err)
	cases := []struct {
		name    string
		asm     string
		wantErr bool
	}{
		{"plain module builds", "TEXT ·answer(SB),$0-8\n\tMOVD $42, R0\n\tMOVD R0, ret+0(FP)\n\tRET\n", false},
		{"assembly include of a protected file is denied", "#include \"" + includePath + "\"\nTEXT ·answer(SB),$0-8\n\tMOVD $SECRET, R0\n\tMOVD R0, ret+0(FP)\n\tRET\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(filepath.Join(mod, "go.mod"), []byte("module probebuild\n\ngo 1.26\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(mod, "main.go"), []byte("package main\n\nfunc answer() int\n\nfunc main() { println(answer()) }\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(mod, "answer_arm64.s"), []byte("#include \"textflag.h\"\n"+c.asm), 0o600))
			logs := t.TempDir()
			err := Build(t.Context(), BuildSpec{
				SourceDir: mod, ModuleDir: mod, Package: ".", Out: filepath.Join(t.TempDir(), "probe"),
				GOROOT: goroot, ModCache: modCache, WorkDir: t.TempDir(), LogFile: filepath.Join(logs, "build.log"),
			}, (*exec.Cmd).Run)
			log, _ := os.ReadFile(filepath.Join(logs, "build.log"))
			if !c.wantErr {
				require.NoError(t, err, string(log))
				return
			}
			// Control: the same build outside the sandbox reads the protected
			// file, so the sandboxed failure is the sandbox refusing it.
			control := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "control"), ".")
			control.Dir = mod
			controlOut, controlErr := control.CombinedOutput()
			require.NoError(t, controlErr, "the unsandboxed control must succeed to prove the include works: %s", controlOut)
			require.Error(t, err, "the sandboxed build must not read the protected file")
			assert.Contains(t, string(log), "#include", "the build failed at the include:\n%s", log)
		})
	}
}

// goEnv returns one go env value.
func goEnv(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command("go", "env", name).Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func TestBuildReadsOnlyItsSourceDir(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the sandbox is implemented with sandbox-exec on macOS")
	}
	goroot, modCache := goEnv(t, "GOROOT"), goEnv(t, "GOMODCACHE")
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "extra.go"), []byte("package main\n\nfunc extra() int { return 1 }\n"), 0o600))

	// newTree writes a checkout holding an app module that replaces a sibling
	// lib module, plus unused links in docs/ that point outside or nowhere.
	newTree := func(t *testing.T) string {
		root := t.TempDir()
		files := map[string]string{
			"lib/go.mod":  "module example.com/lib\n\ngo 1.26\n",
			"lib/lib.go":  "package lib\n\nfunc V() int { return 7 }\n",
			"app/go.mod":  "module example.com/app\n\ngo 1.26\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib => ../lib\n",
			"app/main.go": "package main\n\nimport \"example.com/lib\"\n\nfunc main() { println(lib.V()) }\n",
		}
		for name, content := range files {
			path := filepath.Join(root, filepath.FromSlash(name))
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		}
		require.NoError(t, os.MkdirAll(filepath.Join(root, "docs"), 0o700))
		require.NoError(t, os.Symlink(outside, filepath.Join(root, "docs", "outside")))
		require.NoError(t, os.Symlink("missing.md", filepath.Join(root, "docs", "dangling.md")))
		return root
	}
	build := func(t *testing.T, sourceDir, moduleDir string) (string, error) {
		logs := t.TempDir()
		err := Build(t.Context(), BuildSpec{
			SourceDir: sourceDir, ModuleDir: moduleDir, Package: ".", Out: filepath.Join(t.TempDir(), "app"),
			GOROOT: goroot, ModCache: modCache, WorkDir: t.TempDir(), LogFile: filepath.Join(logs, "build.log"),
		}, (*exec.Cmd).Run)
		log, _ := os.ReadFile(filepath.Join(logs, "build.log"))
		return string(log), err
	}
	// unsandboxed builds moduleDir with the host toolchain, proving that a
	// sandboxed failure comes from the sandbox and not from broken source.
	unsandboxed := func(t *testing.T, moduleDir string) {
		cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "control"), ".")
		cmd.Dir = moduleDir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "the unsandboxed control must succeed: %s", out)
	}

	t.Run("sibling replace inside the source tree builds despite unused outside links", func(t *testing.T) {
		root := newTree(t)
		log, err := build(t, root, filepath.Join(root, "app"))
		require.NoError(t, err, log)
	})
	t.Run("source file linked outside the source tree is denied", func(t *testing.T) {
		root := newTree(t)
		require.NoError(t, os.Symlink(filepath.Join(outside, "extra.go"), filepath.Join(root, "app", "extra.go")))
		unsandboxed(t, filepath.Join(root, "app"))
		log, err := build(t, root, filepath.Join(root, "app"))
		require.Error(t, err, "the sandboxed build must not read the linked outside file")
		assert.Contains(t, log, "extra.go", "the build failed reading the linked file:\n%s", log)
	})
	t.Run("replace leaving the source tree is denied", func(t *testing.T) {
		root := newTree(t)
		unsandboxed(t, filepath.Join(root, "app"))
		log, err := build(t, filepath.Join(root, "app"), filepath.Join(root, "app"))
		require.Error(t, err, "lib is outside SourceDir, so the sandbox must deny reading it")
		assert.Contains(t, log, "lib", "the build failed reading the replaced module:\n%s", log)
	})
	t.Run("module directory outside the source tree is rejected", func(t *testing.T) {
		root := newTree(t)
		_, err := build(t, filepath.Join(root, "lib"), filepath.Join(root, "app"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is outside source directory")
	})
}
