//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zigai/gotmux/tmux"

	integration "github.com/zigai/sesh/internal/integration/tmux"
)

const (
	defaultSessionName = "guest-lifecycle-fixture"
	defaultAlphaFile   = "/home/sesh/fixture-alpha.txt"
	defaultBetaFile    = "/home/sesh/fixture-beta.txt"
	createPollInterval = 50 * time.Millisecond
	createPollBudget   = 5 * time.Second
	verifyPollInterval = 100 * time.Millisecond
	verifyPollBudget   = 10 * time.Second
	terminalWidth      = 80
	terminalHeight     = 24
)

var (
	errOpenServer             = errors.New("open disposable tmux server")
	errUnexpectedVersion      = errors.New("disposable tmux server is not version 3.7c or later 3.x")
	errCreateSession          = errors.New("create disposable tmux session")
	errCreateWindow           = errors.New("create disposable tmux window")
	errSetFixtureEnv          = errors.New("set fixture environment")
	errSessionNotFound        = errors.New("session not found")
	errUsage                  = errors.New("usage: fixture [create|verify] ABSOLUTE_SOCKET [args...]")
	errProcessesTimeout       = errors.New("timeout waiting for fixture pane processes to initialize")
	errWindowCountMismatch    = errors.New("window count mismatch")
	errEditorWindowMissing    = errors.New("editor window not found in restored session")
	errMonitorWindowMissing   = errors.New("monitor window not found in restored session")
	errSleepWindowMissing     = errors.New("sleep window not found in restored session")
	errEditorPaneMissing      = errors.New("editor pane missing")
	errEditorCommandMismatch  = errors.New("editor command mismatch")
	errEditorFileMismatch     = errors.New("editor file mismatch")
	errMonitorPaneMissing     = errors.New("monitor pane missing")
	errMonitorCommandMismatch = errors.New("monitor command mismatch")
	errSleepPaneMissing       = errors.New("sleep pane missing")
	errSleepCommandMismatch   = errors.New("sleep command mismatch")
	errVerifyTimeout          = errors.New("verify restored session timed out")
)

type VerifiedPane struct {
	WindowName string   `json:"windowName"`
	WindowID   string   `json:"windowId"`
	PaneID     string   `json:"paneId"`
	PID        int      `json:"pid"`
	Command    string   `json:"command"`
	Args       []string `json:"args"`
}

type RestoreVerification struct {
	OK          bool           `json:"ok"`
	SessionName string         `json:"sessionName"`
	Windows     []string       `json:"windows"`
	Panes       []VerifiedPane `json:"panes"`
	EditorFile1 string         `json:"editorFile1"`
	EditorFile2 string         `json:"editorFile2"`
}

type fixtureParams struct {
	action      string
	socket      string
	sessionName string
	alphaFile   string
	betaFile    string
}

type windowSet struct {
	editor  tmux.Window
	monitor tmux.Window
	sleep   tmux.Window
	names   []string
}

func parseCreateArgs(args []string) (fixtureParams, error) {
	if len(args) < 3 || len(args) > 4 {
		return fixtureParams{}, errUsage
	}

	sessName := defaultSessionName
	if len(args) == 4 && args[3] != "" {
		sessName = args[3]
	}

	return fixtureParams{
		action:      "create",
		socket:      args[2],
		sessionName: sessName,
		alphaFile:   "",
		betaFile:    "",
	}, nil
}

func parseVerifyArgs(args []string) (fixtureParams, error) {
	if len(args) < 4 || len(args) > 6 {
		return fixtureParams{}, errUsage
	}

	alpha := defaultAlphaFile
	if len(args) >= 5 && args[4] != "" {
		alpha = args[4]
	}

	beta := defaultBetaFile
	if len(args) >= 6 && args[5] != "" {
		beta = args[5]
	}

	return fixtureParams{
		action:      "verify",
		socket:      args[2],
		sessionName: args[3],
		alphaFile:   alpha,
		betaFile:    beta,
	}, nil
}

func parseCommandLine(args []string) (fixtureParams, error) {
	if len(args) < 2 {
		return fixtureParams{}, errUsage
	}

	switch args[1] {
	case "create":
		return parseCreateArgs(args)

	case "verify":
		return parseVerifyArgs(args)

	default:
		sessName := defaultSessionName
		if len(args) == 3 && args[2] != "" {
			sessName = args[2]
		}

		return fixtureParams{
			action:      "create",
			socket:      args[1],
			sessionName: sessName,
			alphaFile:   "",
			betaFile:    "",
		}, nil
	}
}

func main() {
	params, err := parseCommandLine(os.Args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	switch params.action {
	case "create":
		err = runCreate(params.socket, params.sessionName)
	case "verify":
		err = runVerify(params.socket, params.sessionName, params.alphaFile, params.betaFile)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func openVerifiedServer(ctx context.Context, socket string) (*tmux.Server, error) {
	server, err := integration.Open(socket)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errOpenServer, err)
	}

	info, err := server.Probe(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errOpenServer, err)
	}

	if info.Version.Raw != "3.7c" && (info.Version.Major != 3 || info.Version.Minor < 7) {
		return nil, errUnexpectedVersion
	}

	return server, nil
}

func prepareFixtureFiles() (string, string, string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		homeDir = "/home/sesh"
	}

	alphaFile := filepath.Join(homeDir, "fixture-alpha.txt")
	betaFile := filepath.Join(homeDir, "fixture-beta.txt")

	if err := os.WriteFile(alphaFile, []byte("sesh guest fixture alpha\n"), 0o600); err != nil {
		return "", "", "", fmt.Errorf("write fixture alpha: %w", err)
	}

	if err := os.WriteFile(betaFile, []byte("sesh guest fixture beta\n"), 0o600); err != nil {
		return "", "", "", fmt.Errorf("write fixture beta: %w", err)
	}

	return homeDir, alphaFile, betaFile, nil
}

func runCreate(socket, sessionName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	server, err := openVerifiedServer(ctx, socket)
	if err != nil {
		return err
	}

	homeDir, alphaFile, betaFile, err := prepareFixtureFiles()
	if err != nil {
		return err
	}

	session, err := server.NewSession(ctx, tmux.NewSessionOptions{
		Name:    sessionName,
		Dir:     homeDir,
		Window:  "editor",
		Program: tmux.Exec("nvim", "--", alphaFile, betaFile),
		Env:     nil,
		TmuxEnv: nil,
		Size:    tmux.Size{Width: terminalWidth, Height: terminalHeight},
		Start:   tmux.ExistingOnly,
		Group:   "",
	})
	if err != nil {
		return fmt.Errorf("%w: %w", errCreateSession, err)
	}

	if err := addFixtureWindows(ctx, session, homeDir); err != nil {
		return err
	}

	if err := session.Environment().Set(ctx, "SESH_GUEST_EVIDENCE", "synthetic"); err != nil {
		return fmt.Errorf("%w: %w", errSetFixtureEnv, err)
	}

	return waitForFixtureProcesses(ctx, session)
}

func addFixtureWindows(ctx context.Context, session tmux.Session, homeDir string) error {
	optsMonitor := tmux.NewWindowOptions{
		Name:    "monitor",
		Dir:     homeDir,
		Program: tmux.Exec("top"),
		Env:     nil,
		TmuxEnv: nil,
		Index:   nil,
		Before:  false,
		After:   false,
		Select:  false,
	}
	if _, err := session.NewWindow(ctx, optsMonitor); err != nil {
		return fmt.Errorf("%w (monitor): %w", errCreateWindow, err)
	}

	optsSleep := tmux.NewWindowOptions{
		Name:    "sleep",
		Dir:     homeDir,
		Program: tmux.Exec("sleep", "600"),
		Env:     nil,
		TmuxEnv: nil,
		Index:   nil,
		Before:  false,
		After:   false,
		Select:  false,
	}
	if _, err := session.NewWindow(ctx, optsSleep); err != nil {
		return fmt.Errorf("%w (sleep): %w", errCreateWindow, err)
	}

	return nil
}

func waitForFixtureProcesses(ctx context.Context, session tmux.Session) error {
	pollDeadline := time.Now().Add(createPollBudget)
	for !checkProcessesRunning(ctx, session) {
		if time.Now().After(pollDeadline) {
			return errProcessesTimeout
		}

		time.Sleep(createPollInterval)
	}

	return nil
}

func checkProcessesRunning(ctx context.Context, session tmux.Session) bool {
	panes, err := session.Panes(ctx)
	if err != nil || len(panes) < 3 {
		return false
	}

	editorOK, monitorOK, sleepOK := false, false, false

	for _, p := range panes {
		if p.PID <= 0 {
			continue
		}

		if _, _, ok := findProcessOrChildren(p.PID, "nvim"); ok {
			editorOK = true
		}

		if _, _, ok := findProcessOrChildren(p.PID, "top"); ok {
			monitorOK = true
		}

		if _, _, ok := findProcessOrChildren(p.PID, "sleep"); ok {
			sleepOK = true
		}
	}

	return editorOK && monitorOK && sleepOK
}

func runVerify(socket, sessionName, alphaFile, betaFile string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	server, err := openVerifiedServer(ctx, socket)
	if err != nil {
		return err
	}

	session, err := server.FindSession(ctx, sessionName)
	if err != nil {
		return fmt.Errorf("%w: %q: %w", errSessionNotFound, sessionName, err)
	}

	pollDeadline := time.Now().Add(verifyPollBudget)

	var lastErr error

	var report RestoreVerification

	for {
		report, lastErr = verifySessionState(ctx, session, alphaFile, betaFile)
		if lastErr == nil {
			break
		}

		if time.Now().After(pollDeadline) {
			return fmt.Errorf("%w: %w", errVerifyTimeout, lastErr)
		}

		time.Sleep(verifyPollInterval)
	}

	return outputVerification(report, sessionName)
}

func outputVerification(report RestoreVerification, sessionName string) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	if err := enc.Encode(report); err != nil {
		return fmt.Errorf("encode report: %w", err)
	}

	fmt.Println("--- WINDOWS ---")

	for _, w := range report.Windows {
		fmt.Println(w)
	}

	fmt.Println("--- PANES ---")

	for _, p := range report.Panes {
		fmt.Printf("%s:%s pid=%d cmd=%s args=%s\n", sessionName, p.WindowName, p.PID, p.Command, strings.Join(p.Args, " "))
	}

	return nil
}

func locateWindows(ctx context.Context, session tmux.Session) (windowSet, error) {
	links, err := session.Windows(ctx)
	if err != nil {
		return windowSet{}, fmt.Errorf("list session windows: %w", err)
	}

	if len(links) != 3 {
		return windowSet{}, fmt.Errorf("%w: got %d, want 3", errWindowCountMismatch, len(links))
	}

	var res windowSet

	res.names = make([]string, 0, len(links))

	for _, link := range links {
		win := link.Window()

		winInfo, infoErr := win.Info(ctx)
		if infoErr != nil {
			return windowSet{}, fmt.Errorf("query window info: %w", infoErr)
		}

		res.names = append(res.names, fmt.Sprintf("%d: %s (layout %s)", link.Index(), winInfo.Name, winInfo.Layout))
		switch winInfo.Name {
		case "editor":
			res.editor = win
		case "monitor":
			res.monitor = win
		case "sleep":
			res.sleep = win
		}
	}

	if !res.editor.Valid() {
		return windowSet{}, errEditorWindowMissing
	}

	if !res.monitor.Valid() {
		return windowSet{}, errMonitorWindowMissing
	}

	if !res.sleep.Valid() {
		return windowSet{}, errSleepWindowMissing
	}

	return res, nil
}

func verifyEditorPane(ctx context.Context, win tmux.Window, alphaFile, betaFile string) (VerifiedPane, error) {
	panes, err := win.Panes(ctx)
	if err != nil || len(panes) == 0 {
		return VerifiedPane{}, fmt.Errorf("%w: %w", errEditorPaneMissing, err)
	}

	editorPane := panes[0]

	ePID, eArgs, ok := findProcessOrChildren(editorPane.PID, "nvim")
	if !ok {
		return VerifiedPane{}, fmt.Errorf("%w: pid %d running %q", errEditorCommandMismatch, editorPane.PID, editorPane.CurrentCommand)
	}

	hasAlpha, hasBeta := false, false

	for _, arg := range eArgs {
		if strings.Contains(arg, filepath.Base(alphaFile)) {
			hasAlpha = true
		}

		if strings.Contains(arg, filepath.Base(betaFile)) {
			hasBeta = true
		}
	}

	if !hasAlpha || !hasBeta {
		return VerifiedPane{}, fmt.Errorf("%w: args %v", errEditorFileMismatch, eArgs)
	}

	return VerifiedPane{
		WindowName: "editor",
		WindowID:   string(editorPane.WindowID),
		PaneID:     string(editorPane.ID),
		PID:        ePID,
		Command:    "nvim",
		Args:       eArgs,
	}, nil
}

func verifyMonitorPane(ctx context.Context, win tmux.Window) (VerifiedPane, error) {
	panes, err := win.Panes(ctx)
	if err != nil || len(panes) == 0 {
		return VerifiedPane{}, fmt.Errorf("%w: %w", errMonitorPaneMissing, err)
	}

	monitorPane := panes[0]

	mPID, mArgs, ok := findProcessOrChildren(monitorPane.PID, "top")
	if !ok {
		return VerifiedPane{}, fmt.Errorf("%w: pid %d running %q", errMonitorCommandMismatch, monitorPane.PID, monitorPane.CurrentCommand)
	}

	return VerifiedPane{
		WindowName: "monitor",
		WindowID:   string(monitorPane.WindowID),
		PaneID:     string(monitorPane.ID),
		PID:        mPID,
		Command:    "top",
		Args:       mArgs,
	}, nil
}

func verifySleepPane(ctx context.Context, win tmux.Window) (VerifiedPane, error) {
	panes, err := win.Panes(ctx)
	if err != nil || len(panes) == 0 {
		return VerifiedPane{}, fmt.Errorf("%w: %w", errSleepPaneMissing, err)
	}

	sleepPane := panes[0]

	sPID, sArgs, ok := findProcessOrChildren(sleepPane.PID, "sh")
	if !ok {
		sPID, sArgs, ok = findProcessOrChildren(sleepPane.PID, "bash")
	}

	if !ok {
		return VerifiedPane{}, fmt.Errorf("%w: pid %d running %q", errSleepCommandMismatch, sleepPane.PID, sleepPane.CurrentCommand)
	}

	return VerifiedPane{
		WindowName: "sleep",
		WindowID:   string(sleepPane.WindowID),
		PaneID:     string(sleepPane.ID),
		PID:        sPID,
		Command:    sArgs[0],
		Args:       sArgs,
	}, nil
}

func verifySessionState(ctx context.Context, session tmux.Session, alphaFile, betaFile string) (RestoreVerification, error) {
	ws, err := locateWindows(ctx, session)
	if err != nil {
		return RestoreVerification{}, err
	}

	ePane, err := verifyEditorPane(ctx, ws.editor, alphaFile, betaFile)
	if err != nil {
		return RestoreVerification{}, err
	}

	mPane, err := verifyMonitorPane(ctx, ws.monitor)
	if err != nil {
		return RestoreVerification{}, err
	}

	sPane, err := verifySleepPane(ctx, ws.sleep)
	if err != nil {
		return RestoreVerification{}, err
	}

	sessionInfo, _ := session.Info(ctx)
	sessName := sessionInfo.Name

	if sessName == "" {
		sessName = defaultSessionName
	}

	return RestoreVerification{
		OK:          true,
		SessionName: sessName,
		Windows:     ws.names,
		Panes:       []VerifiedPane{ePane, mPane, sPane},
		EditorFile1: alphaFile,
		EditorFile2: betaFile,
	}, nil
}

func readProcessCmdline(pid int) ([]string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil, fmt.Errorf("read /proc/%d/cmdline: %w", pid, err)
	}

	if len(data) == 0 {
		return nil, nil
	}

	raw := bytes.Split(bytes.TrimRight(data, "\x00"), []byte{0})

	args := make([]string, 0, len(raw))
	for _, p := range raw {
		if len(p) > 0 {
			args = append(args, string(p))
		}
	}

	return args, nil
}

func findProcessOrChildren(pid int, nameMatch string) (int, []string, bool) {
	if pid <= 0 {
		return 0, nil, false
	}

	args, err := readProcessCmdline(pid)
	if err == nil && len(args) > 0 {
		base := filepath.Base(args[0])
		if strings.Contains(base, nameMatch) || strings.Contains(args[0], nameMatch) {
			return pid, args, true
		}
	}

	childrenData, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", pid, pid))
	if err != nil {
		return 0, nil, false
	}

	for field := range strings.FieldsSeq(string(childrenData)) {
		var childPID int
		if _, err := fmt.Sscanf(field, "%d", &childPID); err == nil && childPID > 0 {
			if cPID, cArgs, ok := findProcessOrChildren(childPID, nameMatch); ok {
				return cPID, cArgs, true
			}
		}
	}

	return 0, nil, false
}
