package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	report := flag.String("report", "", "path of the NUL-framed report to write")
	hold := flag.Duration("hold", 0, "how long to stay alive after writing the report")
	flag.Parse()

	if *report == "" {
		fmt.Fprintln(os.Stderr, "launchhelper: -report is required")
		os.Exit(2)
	}

	if err := write(*report, *hold); err != nil {
		fmt.Fprintln(os.Stderr, "launchhelper:", err)
		os.Exit(1)
	}
}

func write(path string, holdDuration time.Duration) error {
	var buf bytes.Buffer
	section(&buf, "pid", []string{strconv.Itoa(os.Getpid())})
	section(&buf, "argv", os.Args)
	section(&buf, "env", os.Environ())

	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()

	if err := os.Rename(tmp, path); err != nil {
		return err
	}

	if holdDuration <= 0 {
		return nil
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	select {
	case <-sigCh:
	case <-time.After(holdDuration):
	}

	return nil
}

func section(buf *bytes.Buffer, name string, entries []string) {
	buf.WriteString(name)
	buf.WriteByte(0)
	buf.WriteString(strconv.Itoa(len(entries)))
	buf.WriteByte(0)

	for _, entry := range entries {
		buf.WriteString(entry)
		buf.WriteByte(0)
	}
}
