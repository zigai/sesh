//go:build integration && linux

package process_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/testsupport/testenv"
)

const (
	envHelperFlag     = "GO_PROCESS_TEST_HELPER"
	envCustomVar      = "CUSTOM_TEST_VAR"
	envEmptyVar       = "EMPTY_TEST_VAR"
	envSecretVar      = "SECRET_DO_NOT_PERSIST"
	envBareEntry      = "BARE_ENV_ENTRY"
	helperTimeoutSec  = 5
	zombieTimeoutSec  = 3
	zombiePollMs      = 2
	reportPrefix      = "REPORT:"
	procDeletedSuffix = " (deleted)"
)

type childReport struct {
	ArgsBase64  []string `json:"argsBase64"`
	Cwd         string   `json:"cwd"`
	CustomVar   string   `json:"customVar"`
	EmptyVarSet bool     `json:"emptyVarSet"`
	EmptyVarVal string   `json:"emptyVarVal"`
	BareEntry   bool     `json:"bareEntry"`
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv(envHelperFlag) != "1" {
		return
	}

	actualCwd, err := os.Getwd()
	if err != nil {
		actualCwd = ""
	}

	emptyVal, emptySet := os.LookupEnv(envEmptyVar)
	bareEntry := slices.Contains(os.Environ(), envBareEntry)

	var childArgsBase64 []string
	if len(os.Args) > 3 {
		childArgsBase64 = make([]string, 0, len(os.Args)-3)
		for _, a := range os.Args[3:] {
			childArgsBase64 = append(childArgsBase64, base64.StdEncoding.EncodeToString([]byte(a)))
		}
	}

	report := childReport{
		ArgsBase64:  childArgsBase64,
		Cwd:         actualCwd,
		CustomVar:   os.Getenv(envCustomVar),
		EmptyVarSet: emptySet,
		EmptyVarVal: emptyVal,
		BareEntry:   bareEntry,
	}

	data, err := json.Marshal(report)
	if err == nil {
		fmt.Printf("%s%s\n", reportPrefix, string(data))
	}

	var buf [1]byte

	_, _ = os.Stdin.Read(buf[:])

	os.Exit(0)
}

func spawnHelper(t *testing.T, customArgs []string, customDir string, extraEnv []string) (*exec.Cmd, childReport) {
	t.Helper()

	selfExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	args := append([]string{"-test.run=^TestHelperProcess$", "--"}, customArgs...)
	cmd := exec.CommandContext(t.Context(), selfExe, args...)

	env := make([]string, 0, 2+len(extraEnv))
	env = append(env, envHelperFlag+"=1", "PATH=/usr/bin:/bin")
	env = append(env, extraEnv...)
	cmd.Env = env

	if customDir != "" {
		cmd.Dir = customDir
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}

	t.Cleanup(func() {
		_ = stdin.Close()

		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}

		_ = cmd.Wait()
	})

	report := waitHelperReady(t, stdout)

	return cmd, report
}

func waitHelperReady(t *testing.T, stdout io.ReadCloser) childReport {
	t.Helper()

	scanner := bufio.NewScanner(stdout)
	reportChan := make(chan childReport, 1)
	done := make(chan struct{})

	defer func() {
		if err := stdout.Close(); err != nil {
			t.Error(err)
		}

		<-done
	}()

	go func() {
		defer close(done)

		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, reportPrefix) {
				var rep childReport

				jsonStr := strings.TrimPrefix(line, reportPrefix)
				if err := json.Unmarshal([]byte(jsonStr), &rep); err == nil {
					reportChan <- rep

					return
				}
			}
		}

		reportChan <- childReport{
			ArgsBase64:  nil,
			Cwd:         "",
			CustomVar:   "",
			EmptyVarSet: false,
			EmptyVarVal: "",
			BareEntry:   false,
		}
	}()

	select {
	case rep := <-reportChan:
		if rep.Cwd == "" && len(rep.ArgsBase64) == 0 {
			t.Fatal("helper process did not report valid readiness")
		}

		return rep
	case <-time.After(helperTimeoutSec * time.Second):
		t.Fatal("timed out waiting for helper process readiness")
	}

	return childReport{
		ArgsBase64:  nil,
		Cwd:         "",
		CustomVar:   "",
		EmptyVarSet: false,
		EmptyVarVal: "",
		BareEntry:   false,
	}
}

func TestObserve_LiveProcess_ExactArgv(t *testing.T) {
	tempDir := t.TempDir()

	rawNonUTF8 := string([]byte{0xff, 0xfe, 'b', 'a', 'd', 'b', 'y', 't', 'e', 's'})
	customArgs := []string{
		"",
		"arg with spaces",
		"arg\twith\ttabs",
		"arg\nwith\nnewlines",
		rawNonUTF8,
	}

	extraEnv := []string{
		envCustomVar + "=hello_world",
		envEmptyVar + "=",
		envSecretVar + "=private_credentials",
		envBareEntry,
	}

	cmd, childRep := spawnHelper(t, customArgs, tempDir, extraEnv)
	pid := cmd.Process.Pid

	birth, err := process.ReadIdentity(t.Context(), pid)
	if err != nil {
		t.Fatalf("ReadIdentity failed: %v", err)
	}

	if birth.IsZero() {
		t.Fatal("expected non-zero birth identity")
	}

	req := process.Request{
		PID:           pid,
		ExpectedBirth: birth,
		EnvironmentKeys: []string{
			envCustomVar,
			envEmptyVar,
			"UNSET_VAR_NAME",
			envBareEntry,
		},
	}

	obs, err := process.Observe(t.Context(), req)
	if err != nil {
		t.Fatalf("Observe failed: %v", err)
	}

	assertObservedMetadata(t, obs, pid, birth, tempDir)
	assertObservedArguments(t, obs.Arguments, customArgs, childRep.ArgsBase64)
	assertObservedEnvironment(t, obs.Environment, childRep)
}

func TestObserveForegroundSelectsShellForegroundChild(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable")
	}

	master, slave := openTestPTY(t)
	cmd := exec.CommandContext(t.Context(), bashPath, "--noprofile", "--norc", "-i")
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "TERM=dumb", "PS1="}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start foreground shell: %v", err)
	}

	if err := slave.Close(); err != nil {
		t.Fatalf("close parent slave: %v", err)
	}

	t.Cleanup(func() {
		_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
		_ = master.Close()
		_ = cmd.Wait()
	})

	paneBirth, err := process.ReadIdentity(t.Context(), cmd.Process.Pid)
	if err != nil {
		t.Fatalf("read shell identity: %v", err)
	}

	if _, err := master.WriteString("sleep 300\n"); err != nil {
		t.Fatalf("start foreground child: %v", err)
	}

	request := process.ForegroundRequest{
		PanePID: cmd.Process.Pid, ExpectedPaneBirth: paneBirth, EnvironmentKeys: []string{"PATH"},
	}
	observation := waitForForegroundChild(t, request)

	if filepath.Base(string(observation.Executable)) != "sleep" {
		t.Fatalf("foreground executable = %q, want sleep", observation.Executable)
	}

	if len(observation.Arguments) != 2 || string(observation.Arguments[1]) != "300" {
		t.Fatalf("foreground arguments = %q, want sleep 300", observation.Arguments)
	}
}

func waitForForegroundChild(t *testing.T, request process.ForegroundRequest) *process.Observation {
	t.Helper()

	deadline := time.Now().Add(helperTimeoutSec * time.Second)

	for {
		observation, observeErr := process.ObserveForeground(t.Context(), request)
		if observeErr == nil && observation.PID != request.PanePID {
			return observation
		}

		if observeErr != nil && !errors.Is(observeErr, process.ErrForegroundUnavailable) &&
			!errors.Is(observeErr, process.ErrForegroundChanged) {
			t.Fatalf("ObserveForeground failed: %v", observeErr)
		}

		if time.Now().After(deadline) {
			t.Fatalf("foreground child was not observed: %v", observeErr)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func openTestPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()

	masterFD, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open PTY master: %v", err)
	}

	master := os.NewFile(uintptr(masterFD), "pty-master")

	t.Cleanup(func() { _ = master.Close() })

	if err := unix.IoctlSetPointerInt(masterFD, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlock PTY: %v", err)
	}

	ptyNumber, err := unix.IoctlGetInt(masterFD, unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("read PTY number: %v", err)
	}

	slave, err := os.OpenFile(filepath.Join("/dev/pts", strconv.Itoa(ptyNumber)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open PTY slave: %v", err)
	}

	t.Cleanup(func() { _ = slave.Close() })

	return master, slave
}

func assertObservedMetadata(t *testing.T, obs *process.Observation, pid int, birth process.Identity, tempDir string) {
	t.Helper()

	if obs.PID != pid {
		t.Errorf("expected PID %d, got %d", pid, obs.PID)
	}

	if obs.Birth != birth {
		t.Errorf("expected birth %v, got %v", birth, obs.Birth)
	}

	if obs.ExecutableDeleted {
		t.Errorf("expected ExecutableDeleted false")
	}

	if obs.DirectoryDeleted {
		t.Errorf("expected DirectoryDeleted false")
	}

	realDir, err := filepath.EvalSymlinks(tempDir)
	if err != nil {
		realDir = tempDir
	}

	if string(obs.Directory) != realDir {
		t.Errorf("expected Directory %q, got %q", realDir, string(obs.Directory))
	}
}

func assertObservedArguments(t *testing.T, got [][]byte, expected []string, childArgsBase64 []string) {
	t.Helper()

	if len(got) < len(expected) {
		t.Fatalf("too few arguments: got %d", len(got))
	}

	trailing := got[len(got)-len(expected):]
	for i, exp := range expected {
		actual := string(trailing[i])
		if actual != exp {
			t.Errorf("arg %d: expected %q, got %q", i, exp, actual)
		}
	}

	if len(childArgsBase64) != len(expected) {
		t.Fatalf("child reported %d args, expected %d", len(childArgsBase64), len(expected))
	}

	for i, exp := range expected {
		decoded, err := base64.StdEncoding.DecodeString(childArgsBase64[i])
		if err != nil {
			t.Fatalf("decode child arg %d: %v", i, err)
		}

		if !bytes.Equal(decoded, []byte(exp)) {
			t.Errorf("child arg %d mismatch: expected %q, got %q", i, exp, string(decoded))
		}
	}
}

func assertObservedEnvironment(t *testing.T, env []model.Variable, rep childReport) {
	t.Helper()

	if len(env) != 4 {
		t.Fatalf("expected 4 environment variables, got %d", len(env))
	}

	envMap := make(map[string]model.Variable, len(env))
	for _, v := range env {
		envMap[string(v.Name)] = v
	}

	assertEnvVar(t, envMap, envCustomVar, "set", "hello_world")
	assertEnvVar(t, envMap, envEmptyVar, "set", "")
	assertEnvVar(t, envMap, "UNSET_VAR_NAME", "absent", "")
	assertEnvVar(t, envMap, envBareEntry, "absent", "")

	if rep.CustomVar != "hello_world" {
		t.Errorf("child reported custom var %q, expected hello_world", rep.CustomVar)
	}

	if !rep.EmptyVarSet || rep.EmptyVarVal != "" {
		t.Errorf("child reported empty var state unexpected: %+v", rep)
	}

	if !rep.BareEntry {
		t.Error("child did not receive the bare environment entry")
	}

	if _, secretFound := envMap[envSecretVar]; secretFound {
		t.Errorf("secret variable %s was persisted unexpectedly", envSecretVar)
	}
}

func assertEnvVar(t *testing.T, envMap map[string]model.Variable, name, expectedState, expectedVal string) {
	t.Helper()

	v, ok := envMap[name]
	if !ok {
		t.Errorf("variable %s was not found in environment", name)

		return
	}

	if v.State != expectedState {
		t.Errorf("variable %s state: expected %s, got %s", name, expectedState, v.State)
	}

	if expectedState == "absent" && v.Value != nil {
		t.Errorf("variable %s: expected nil value for absent state, got %v", name, v.Value)
	}

	if expectedState == "set" && string(v.Value) != expectedVal {
		t.Errorf("variable %s value: expected %q, got %q", name, expectedVal, string(v.Value))
	}
}

func copyTestBinary(t *testing.T, dst string) {
	t.Helper()

	origBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	srcFile, err := os.Open(origBinary)
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	defer func() {
		_ = srcFile.Close()
	}()

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		t.Fatalf("open dst: %v", err)
	}
	defer func() {
		_ = dstFile.Close()
	}()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		t.Fatalf("copy binary: %v", err)
	}
}

func spawnBinaryHelper(t *testing.T, binary string) *exec.Cmd {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestHelperProcess$", "--")
	cmd.Env = []string{testenv.IsolatedMarker + "=1", envHelperFlag + "=1", "PATH=/usr/bin:/bin"}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start copied helper: %v", err)
	}

	t.Cleanup(func() {
		_ = stdin.Close()

		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}

		_ = cmd.Wait()
	})

	_ = waitHelperReady(t, stdout)

	return cmd
}

func TestObserve_LiveProcess_DeletedExecutable(t *testing.T) {
	tempDir := t.TempDir()

	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	copiedBinary := filepath.Join(binDir, "copied_helper")
	copyTestBinary(t, copiedBinary)

	cmd := spawnBinaryHelper(t, copiedBinary)
	pid := cmd.Process.Pid

	birth, err := process.ReadIdentity(t.Context(), pid)
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}

	if err := os.Remove(copiedBinary); err != nil {
		t.Fatalf("remove binary: %v", err)
	}

	obs, err := process.Observe(t.Context(), process.Request{
		PID:             pid,
		ExpectedBirth:   birth,
		EnvironmentKeys: nil,
	})
	if err != nil {
		t.Fatalf("Observe with deleted executable failed: %v", err)
	}

	if !obs.ExecutableDeleted {
		t.Errorf("expected ExecutableDeleted true for unlinked binary")
	}

	realCopiedBinary, err := filepath.EvalSymlinks(binDir)
	if err == nil {
		realCopiedBinary = filepath.Join(realCopiedBinary, "copied_helper")
	} else {
		realCopiedBinary = copiedBinary
	}

	if string(obs.Executable) != realCopiedBinary {
		t.Errorf("expected Executable %q, got %q", realCopiedBinary, string(obs.Executable))
	}
}

func TestObserve_LiveProcess_LiteralDeletedSuffixDirectoryIsAmbiguous(t *testing.T) {
	tempDir := t.TempDir()

	literalDir := filepath.Join(tempDir, "workdir (deleted)")
	if err := os.MkdirAll(literalDir, 0o700); err != nil {
		t.Fatalf("mkdir literalDir: %v", err)
	}

	cmd, _ := spawnHelper(t, nil, literalDir, nil)
	pid := cmd.Process.Pid

	birth, err := process.ReadIdentity(t.Context(), pid)
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}

	_, err = process.Observe(t.Context(), process.Request{
		PID:             pid,
		ExpectedBirth:   birth,
		EnvironmentKeys: nil,
	})
	if !errors.Is(err, process.ErrAmbiguousProcLink) {
		t.Fatalf("Observe error = %v, want %v", err, process.ErrAmbiguousProcLink)
	}
}

func TestObserve_LiveProcess_LiteralDeletedSuffixExecutableIsAmbiguous(t *testing.T) {
	tempDir := t.TempDir()

	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	literalBinary := filepath.Join(binDir, "copied_helper (deleted)")
	copyTestBinary(t, literalBinary)

	cmd := spawnBinaryHelper(t, literalBinary)
	pid := cmd.Process.Pid

	birth, err := process.ReadIdentity(t.Context(), pid)
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}

	_, err = process.Observe(t.Context(), process.Request{
		PID:             pid,
		ExpectedBirth:   birth,
		EnvironmentKeys: nil,
	})
	if !errors.Is(err, process.ErrAmbiguousProcLink) {
		t.Fatalf("Observe error = %v, want %v", err, process.ErrAmbiguousProcLink)
	}
}

func TestObserve_LiveProcess_DeletedExecutableWithLiteralSuffixAliasIsAmbiguous(t *testing.T) {
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	executedPath := filepath.Join(binDir, "copied_helper")
	literalSuffixAlias := executedPath + procDeletedSuffix
	copyTestBinary(t, executedPath)

	if err := os.Link(executedPath, literalSuffixAlias); err != nil {
		t.Fatalf("create same-inode literal suffix alias: %v", err)
	}

	cmd := spawnBinaryHelper(t, executedPath)

	birth, err := process.ReadIdentity(t.Context(), cmd.Process.Pid)
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}

	if err := os.Remove(executedPath); err != nil {
		t.Fatalf("remove executed pathname: %v", err)
	}

	_, err = process.Observe(t.Context(), process.Request{
		PID:             cmd.Process.Pid,
		ExpectedBirth:   birth,
		EnvironmentKeys: nil,
	})
	if !errors.Is(err, process.ErrAmbiguousProcLink) {
		t.Fatalf("Observe error = %v, want %v", err, process.ErrAmbiguousProcLink)
	}
}

func TestObserve_LiveProcess_DeletedLiteralSuffixExecutablePreservesLiteralName(t *testing.T) {
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	literalBinary := filepath.Join(binDir, "copied_helper"+procDeletedSuffix)
	copyTestBinary(t, literalBinary)

	cmd := spawnBinaryHelper(t, literalBinary)

	birth, err := process.ReadIdentity(t.Context(), cmd.Process.Pid)
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}

	if err := os.Remove(literalBinary); err != nil {
		t.Fatalf("remove literal suffix executable: %v", err)
	}

	obs, err := process.Observe(t.Context(), process.Request{
		PID:             cmd.Process.Pid,
		ExpectedBirth:   birth,
		EnvironmentKeys: nil,
	})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if !obs.ExecutableDeleted {
		t.Fatal("deleted literal suffix executable was not marked deleted")
	}

	realBinDir, err := filepath.EvalSymlinks(binDir)
	if err != nil {
		t.Fatalf("resolve bin directory: %v", err)
	}

	wantPath := filepath.Join(realBinDir, "copied_helper"+procDeletedSuffix)
	if string(obs.Executable) != wantPath {
		t.Fatalf("Executable = %q, want %q", obs.Executable, wantPath)
	}
}

func TestObserve_LiveProcess_DeletedDirectory(t *testing.T) {
	tempDir := t.TempDir()

	workingDir := filepath.Join(tempDir, "workdir")
	if err := os.MkdirAll(workingDir, 0o700); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	cmd, _ := spawnHelper(t, nil, workingDir, nil)
	pid := cmd.Process.Pid

	birth, err := process.ReadIdentity(t.Context(), pid)
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}

	if err := os.Remove(workingDir); err != nil {
		t.Fatalf("remove working dir: %v", err)
	}

	obs, err := process.Observe(t.Context(), process.Request{
		PID:             pid,
		ExpectedBirth:   birth,
		EnvironmentKeys: nil,
	})
	if err != nil {
		t.Fatalf("Observe with deleted cwd failed: %v", err)
	}

	if !obs.DirectoryDeleted {
		t.Errorf("expected DirectoryDeleted true for unlinked cwd")
	}

	realWorkingDir, err := filepath.EvalSymlinks(tempDir)
	if err == nil {
		realWorkingDir = filepath.Join(realWorkingDir, "workdir")
	} else {
		realWorkingDir = workingDir
	}

	if string(obs.Directory) != realWorkingDir {
		t.Errorf("expected Directory %q, got %q", realWorkingDir, string(obs.Directory))
	}
}

func TestObserve_LiveProcess_ZombieProcess(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start zombie command: %v", err)
	}

	pid := cmd.Process.Pid

	t.Cleanup(func() {
		_ = cmd.Wait()
	})

	deadline := time.Now().Add(zombieTimeoutSec * time.Second)
	zombieReady := false

	for time.Now().Before(deadline) {
		statData, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err == nil {
			_, after, ok := bytes.CutLast(statData, []byte(") "))
			if ok {
				fields := bytes.Fields(after)
				if len(fields) > 0 && len(fields[0]) == 1 && fields[0][0] == 'Z' {
					zombieReady = true

					break
				}
			}
		}

		time.Sleep(zombiePollMs * time.Millisecond)
	}

	if !zombieReady {
		t.Fatal("process did not enter observable zombie state within deadline")
	}

	birth := process.Identity{BootID: testBootID, StartTime: testStartTime}

	_, err := process.Observe(t.Context(), process.Request{
		PID:             pid,
		ExpectedBirth:   birth,
		EnvironmentKeys: nil,
	})
	if err == nil {
		t.Fatal("expected error observing zombie process, got nil")
	}

	if !errors.Is(err, process.ErrProcessZombie) && !errors.Is(err, process.ErrProcessVanished) {
		t.Errorf("expected ErrProcessZombie or ErrProcessVanished, got: %v", err)
	}
}

func TestObserve_VanishedProcess(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run command: %v", err)
	}

	vanishedPID := cmd.Process.Pid

	birth := process.Identity{BootID: testBootID, StartTime: testStartTime}

	_, err := process.Observe(t.Context(), process.Request{
		PID:             vanishedPID,
		ExpectedBirth:   birth,
		EnvironmentKeys: nil,
	})
	if err == nil {
		t.Fatal("expected error observing vanished PID, got nil")
	}

	if !errors.Is(err, process.ErrProcessVanished) {
		t.Errorf("expected ErrProcessVanished, got: %v", err)
	}
}

func TestObserve_PIDReuse_IdentityMismatch(t *testing.T) {
	cmd, _ := spawnHelper(t, nil, "", nil)
	pid := cmd.Process.Pid

	birth, err := process.ReadIdentity(t.Context(), pid)
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}

	mismatchedStartTime := process.Identity{
		BootID:    birth.BootID,
		StartTime: birth.StartTime + 99999,
	}

	_, err = process.Observe(t.Context(), process.Request{
		PID:             pid,
		ExpectedBirth:   mismatchedStartTime,
		EnvironmentKeys: nil,
	})
	if !errors.Is(err, process.ErrIdentityMismatch) {
		t.Errorf("expected ErrIdentityMismatch for start time mismatch, got: %v", err)
	}

	mismatchedBootID := process.Identity{
		BootID:    "00000000-0000-0000-0000-000000000000",
		StartTime: birth.StartTime,
	}

	_, err = process.Observe(t.Context(), process.Request{
		PID:             pid,
		ExpectedBirth:   mismatchedBootID,
		EnvironmentKeys: nil,
	})
	if !errors.Is(err, process.ErrIdentityMismatch) {
		t.Errorf("expected ErrIdentityMismatch for boot ID mismatch, got: %v", err)
	}
}

func TestObserve_ContextCanceled(t *testing.T) {
	cmd, _ := spawnHelper(t, nil, "", nil)
	pid := cmd.Process.Pid

	birth, err := process.ReadIdentity(t.Context(), pid)
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = process.Observe(ctx, process.Request{
		PID:             pid,
		ExpectedBirth:   birth,
		EnvironmentKeys: nil,
	})
	if err == nil {
		t.Fatal("expected error on canceled context, got nil")
	}

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}
