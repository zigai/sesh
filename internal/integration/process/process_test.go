package process_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/integration/process"
)

const (
	testBootID    = "11111111-2222-3333-4444-555555555555"
	testStartTime = 123456
	testPID       = 42
)

func TestIdentity_ParsingAndFormatting(t *testing.T) {
	id := process.Identity{BootID: testBootID, StartTime: testStartTime}
	expectedStr := testBootID + ":" + strconv.Itoa(testStartTime)

	if id.String() != expectedStr {
		t.Errorf("expected %q, got %q", expectedStr, id.String())
	}

	if string(id.Bytes()) != expectedStr {
		t.Errorf("expected bytes %q, got %q", expectedStr, string(id.Bytes()))
	}

	parsed, err := process.ParseIdentity(expectedStr)
	if err != nil {
		t.Fatalf("ParseIdentity failed: %v", err)
	}

	if parsed != id {
		t.Errorf("expected parsed %+v, got %+v", id, parsed)
	}

	parsedZeroStart, err := process.ParseIdentity(testBootID + ":0")
	if err != nil {
		t.Fatalf("ParseIdentity failed for StartTime=0: %v", err)
	}

	if parsedZeroStart.StartTime != 0 || parsedZeroStart.BootID != testBootID {
		t.Errorf("expected StartTime=0, got %+v", parsedZeroStart)
	}

	var zero process.Identity
	if !zero.IsZero() {
		t.Error("expected zero identity to report IsZero() true")
	}

	if zero.String() != "" {
		t.Errorf("expected zero String() empty, got %q", zero.String())
	}

	malformedCases := []string{
		"",
		"no_colon",
		":1234",
		"boot_id:",
		"11111111-2222-3333-4444-555555555555:not_a_number",
		"11111111-2222-3333-4444-555555555555:0123",
		"11111111-2222-3333-4444-555555555555:+123",
		"UPPERCASE-2222-3333-4444-555555555555:123",
		"short-uuid:123",
		"too:many:colons",
	}
	for _, tc := range malformedCases {
		_, err := process.ParseIdentity(tc)
		if !errors.Is(err, process.ErrMalformedIdentity) {
			t.Errorf("expected ErrMalformedIdentity for %q, got: %v", tc, err)
		}
	}
}

func TestObserve_ValidationPreconditions(t *testing.T) {
	validBirth := process.Identity{BootID: testBootID, StartTime: testStartTime}

	tests := []struct {
		name    string
		req     process.Request
		wantErr error
	}{
		{
			name: "non-positive PID zero",
			req: process.Request{
				PID:             0,
				ExpectedBirth:   validBirth,
				EnvironmentKeys: nil,
			},
			wantErr: process.ErrInvalidPID,
		},
		{
			name: "non-positive PID negative",
			req: process.Request{
				PID:             -1,
				ExpectedBirth:   validBirth,
				EnvironmentKeys: nil,
			},
			wantErr: process.ErrInvalidPID,
		},
		{
			name: "missing birth identity precondition",
			req: process.Request{
				PID:             testPID,
				ExpectedBirth:   process.Identity{BootID: "", StartTime: 0},
				EnvironmentKeys: nil,
			},
			wantErr: process.ErrBirthRequired,
		},
		{
			name: "missing boot ID with non-zero start time",
			req: process.Request{
				PID:             testPID,
				ExpectedBirth:   process.Identity{BootID: "", StartTime: 123},
				EnvironmentKeys: nil,
			},
			wantErr: process.ErrBirthRequired,
		},
		{
			name: "malformed boot ID not canonical UUID",
			req: process.Request{
				PID:             testPID,
				ExpectedBirth:   process.Identity{BootID: "not-a-valid-uuid", StartTime: 123},
				EnvironmentKeys: nil,
			},
			wantErr: process.ErrMalformedIdentity,
		},
		{
			name: "invalid environment variable name empty",
			req: process.Request{
				PID:             testPID,
				ExpectedBirth:   validBirth,
				EnvironmentKeys: []string{""},
			},
			wantErr: process.ErrInvalidVariableName,
		},
		{
			name: "invalid environment variable name containing equals",
			req: process.Request{
				PID:             testPID,
				ExpectedBirth:   validBirth,
				EnvironmentKeys: []string{"FOO=BAR"},
			},
			wantErr: process.ErrInvalidVariableName,
		},
		{
			name: "invalid environment variable name containing NUL",
			req: process.Request{
				PID:             testPID,
				ExpectedBirth:   validBirth,
				EnvironmentKeys: []string{"FOO\x00BAR"},
			},
			wantErr: process.ErrInvalidVariableName,
		},
		{
			name: "too many environment keys exceeding limit",
			req: process.Request{
				PID:             testPID,
				ExpectedBirth:   validBirth,
				EnvironmentKeys: make([]string, process.MaxEnvironmentKeys+1),
			},
			wantErr: process.ErrEnvironmentLimitExceeded,
		},
		{
			name: "environment key exceeding length limit",
			req: process.Request{
				PID:             testPID,
				ExpectedBirth:   validBirth,
				EnvironmentKeys: []string{strings.Repeat("A", process.MaxEnvironmentKeyLength+1)},
			},
			wantErr: process.ErrEnvironmentLimitExceeded,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := process.Observe(context.Background(), tc.req)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestObserve_ValidationPreconditions_StartTimeZeroAllowed(t *testing.T) {
	earlyBootBirth := process.Identity{BootID: testBootID, StartTime: 0}
	req := process.Request{
		PID:             1,
		ExpectedBirth:   earlyBootBirth,
		EnvironmentKeys: nil,
	}

	_, err := process.Observe(context.Background(), req)
	if errors.Is(err, process.ErrBirthRequired) || errors.Is(err, process.ErrMalformedIdentity) {
		t.Errorf("expected validation to permit StartTime=0, got %v", err)
	}
}

func TestObserveForegroundPreservesCancellationClassification(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := process.ObserveForeground(ctx, process.ForegroundRequest{
		PanePID: 1,
		ExpectedPaneBirth: process.Identity{
			BootID: testBootID, StartTime: 1,
		},
		EnvironmentKeys: nil,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ObserveForeground error = %v, want context cancellation", err)
	}

	diagnostic, ok := oops.AsOops(err)
	if !ok || diagnostic.Code() != process.CodeCanceled {
		t.Fatalf("ObserveForeground code = %q, want %q", diagnostic.Code(), process.CodeCanceled)
	}
}

func TestErrorWrapping_PrivacyAndDiagnostics(t *testing.T) {
	_, err := process.ReadIdentity(context.Background(), 0)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	oopsErr, ok := oops.AsOops(err)
	if !ok {
		t.Fatal("expected error to be enriched with oops")
	}

	if oopsErr.Domain() != process.Domain {
		t.Errorf("expected domain %q, got %q", process.Domain, oopsErr.Domain())
	}

	if oopsErr.Code() != process.CodeInvalidPID {
		t.Errorf("expected code %q, got %q", process.CodeInvalidPID, oopsErr.Code())
	}

	secretPayload := "SUPER_SECRET_TOKEN_XYZ_12345"
	_, err = process.Observe(context.Background(), process.Request{
		PID:           -1,
		ExpectedBirth: process.Identity{BootID: testBootID, StartTime: 1},
		EnvironmentKeys: []string{
			"VALID_NAME",
		},
	})

	if strings.Contains(err.Error(), secretPayload) {
		t.Fatalf("error leaked payload: %s", err.Error())
	}

	_, err = process.Observe(context.Background(), process.Request{
		PID:             testPID,
		ExpectedBirth:   process.Identity{BootID: testBootID, StartTime: testStartTime},
		EnvironmentKeys: nil,
	})
	if err == nil {
		t.Fatal("expected error with non-existent process, got nil")
	}

	if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaked private procfs path: %s", err.Error())
	}

	var pathErr *os.PathError
	if errors.As(err, &pathErr) && pathErr.Path != "[sanitized]" {
		t.Errorf("expected sanitized Path in os.PathError, got %q", pathErr.Path)
	}
}
