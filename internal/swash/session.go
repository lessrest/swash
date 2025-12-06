package swash

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
)

// Session represents a running swash session.
type Session struct {
	ID      string `json:"id"`
	Unit    string `json:"unit"`
	PID     uint32 `json:"pid"`
	CWD     string `json:"cwd"`
	Status  string `json:"status"`
	Command string `json:"command"`
	Started string `json:"started"`
}

// GenSessionID generates a short random session ID like "KXO284".
func GenSessionID() string {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const digits = "0123456789"

	b := make([]byte, 6)
	rand.Read(b)

	id := make([]byte, 6)
	for i := 0; i < 3; i++ {
		id[i] = letters[int(b[i])%len(letters)]
	}
	for i := 3; i < 6; i++ {
		id[i] = digits[int(b[i])%len(digits)]
	}
	return string(id)
}

func UnitName(sessionID string) string {
	return fmt.Sprintf("swash-host-%s.service", sessionID)
}

// ListSessions returns all running swash sessions using the systemd D-Bus API.
func ListSessions() ([]Session, error) {
	ctx := context.Background()
	conn, err := dbus.NewUserConnectionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("connecting to systemd: %w", err)
	}
	defer conn.Close()

	// List all swash-host-*.service units
	units, err := conn.ListUnitsByPatternsContext(
		ctx,
		[]string{"active", "activating"},
		[]string{"swash-host-*.service"},
	)
	if err != nil {
		return nil, fmt.Errorf("listing units: %w", err)
	}

	var sessions []Session
	for _, u := range units {
		// Extract session ID from unit name: swash-host-ABC123.service -> ABC123
		sessionID := strings.TrimPrefix(strings.TrimSuffix(u.Name, ".service"), "swash-host-")

		// Get unit properties (for timestamps, description)
		unitProps, err := conn.GetUnitPropertiesContext(ctx, u.Name)
		if err != nil {
			continue
		}

		// Get service properties (for MainPID, ExecMainStatus)
		serviceProps, err := conn.GetUnitTypePropertiesContext(ctx, u.Name, "Service")
		if err != nil {
			continue
		}

		status := "running"
		if exitStatus, ok := serviceProps["ExecMainStatus"].(int32); ok && exitStatus != 0 {
			status = "exited"
		}

		var pid uint32
		if mainPID, ok := serviceProps["MainPID"].(uint32); ok {
			pid = mainPID
		}

		var cwd string
		if workDir, ok := serviceProps["WorkingDirectory"].(string); ok {
			cwd = workDir
		}

		var command string
		if desc, ok := unitProps["Description"].(string); ok {
			command = desc
		}

		var started string
		if ts, ok := unitProps["ActiveEnterTimestamp"].(uint64); ok && ts > 0 {
			// Convert microseconds to time
			t := time.Unix(int64(ts/1000000), int64((ts%1000000)*1000))
			started = t.Format("Mon 2006-01-02 15:04:05 MST")
		}

		sessions = append(sessions, Session{
			ID:      sessionID,
			Unit:    u.Name,
			PID:     pid,
			CWD:     cwd,
			Status:  status,
			Command: command,
			Started: started,
		})
	}
	return sessions, nil
}

// StartSession starts a new swash session with the given command.
// serverBinary is the path to the swash-server binary.
func StartSession(command []string, serverBinary string) (string, error) {
	sessionID := GenSessionID()
	unit := UnitName(sessionID)
	cwd, _ := os.Getwd()
	dbusName := fmt.Sprintf("%s.%s", DBusNamePrefix, sessionID)

	cmdStr := strings.Join(command, " ")
	args := []string{
		"systemd-run", "--user",
		"--collect",
		"--unit=" + unit,
		"--slice=swash-" + sessionID + ".slice",
		"--service-type=dbus",
		"--property=BusName=" + dbusName,
		"--property=StandardOutput=journal",
		"--property=StandardError=journal",
		"--working-directory=" + cwd,
		"--description=" + cmdStr,
	}

	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "_") {
			continue
		}
		args = append(args, "--setenv="+env)
	}

	args = append(args, "--", serverBinary,
		"--session", sessionID,
		"--command-json", MustJSON(command))

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("systemd-run: %w", err)
	}

	return sessionID, nil
}

// StopSession stops a session by ID using the systemd D-Bus API.
func StopSession(sessionID string) error {
	ctx := context.Background()
	conn, err := dbus.NewUserConnectionContext(ctx)
	if err != nil {
		return fmt.Errorf("connecting to systemd: %w", err)
	}
	defer conn.Close()

	// StopUnit returns a channel that receives the job result
	resultChan := make(chan string, 1)
	_, err = conn.StopUnitContext(ctx, UnitName(sessionID), "replace", resultChan)
	if err != nil {
		return fmt.Errorf("stopping unit: %w", err)
	}

	// Wait for the job to complete
	result := <-resultChan
	if result != "done" {
		return fmt.Errorf("stop job failed: %s", result)
	}
	return nil
}

// KillSession sends SIGKILL to the process in a session.
func KillSession(sessionID string) error {
	ctx := context.Background()
	conn, err := dbus.NewUserConnectionContext(ctx)
	if err != nil {
		return fmt.Errorf("connecting to systemd: %w", err)
	}
	defer conn.Close()

	// KillUnitContext sends a signal to all of the unit's processes
	// int32(9) is SIGKILL
	conn.KillUnitWithTarget(ctx, UnitName(sessionID), dbus.All, int32(9))
	return nil
}

// SendInput sends input to the process via the swash D-Bus service.
func SendInput(sessionID string, input string) error {
	// TODO: implement via godbus calling our SwashService.SendInput
	return fmt.Errorf("not implemented")
}

func MustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
