package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

// Unit represents a running or completed process unit
type Unit struct {
	Name        string
	Description string
	Command     []string
	WorkingDir  string
	Environment []string
	BusName     string // For Type=dbus services
	Slice       string // Slice this unit belongs to

	// Unit dependencies
	BindsTo      []string   // Units this depends on - stops if any of these stop
	After        []string   // Units this should start after
	ExecStopPost [][]string // Commands to run after main process exits

	// Runtime state
	Cmd        *exec.Cmd
	PID        int
	StartedAt  time.Time
	ExitStatus int
	State      string // "running", "exited", "failed"

	// In-memory logs
	Stdout []LogEntry
	Stderr []LogEntry
}

// LogEntry is a single log line with metadata
type LogEntry struct {
	Timestamp time.Time
	Stream    string // "stdout" or "stderr"
	Data      string
}

// Manager handles process lifecycle and logs
type Manager struct {
	mu      sync.RWMutex
	units   map[string]*Unit
	conn    *dbus.Conn
	journal *JournalService
	jobID   uint32
}

// UnitObject wraps a Unit for D-Bus export with Properties interface.
type UnitObject struct {
	manager *Manager
	name    string
}

// Get implements org.freedesktop.DBus.Properties.Get
func (u *UnitObject) Get(iface, prop string) (dbus.Variant, *dbus.Error) {
	u.manager.mu.RLock()
	defer u.manager.mu.RUnlock()

	unit, ok := u.manager.units[u.name]
	if !ok {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.systemd1.NoSuchUnit", []interface{}{"unit not found"})
	}

	switch iface {
	case "org.freedesktop.systemd1.Unit":
		switch prop {
		case "Id":
			return dbus.MakeVariant(u.name), nil
		case "Description":
			return dbus.MakeVariant(unit.Description), nil
		case "ActiveState":
			state := "active"
			if unit.State == "exited" {
				state = "inactive"
			} else if unit.State == "failed" {
				state = "failed"
			}
			return dbus.MakeVariant(state), nil
		case "ActiveEnterTimestamp":
			return dbus.MakeVariant(uint64(unit.StartedAt.UnixMicro())), nil
		}
	case "org.freedesktop.systemd1.Service":
		switch prop {
		case "MainPID":
			return dbus.MakeVariant(uint32(unit.PID)), nil
		case "WorkingDirectory":
			return dbus.MakeVariant(unit.WorkingDir), nil
		case "ExecMainStatus":
			return dbus.MakeVariant(int32(unit.ExitStatus)), nil
		}
	}

	return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.UnknownProperty", []interface{}{prop})
}

// GetAll implements org.freedesktop.DBus.Properties.GetAll
func (u *UnitObject) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	u.manager.mu.RLock()
	defer u.manager.mu.RUnlock()

	unit, ok := u.manager.units[u.name]
	if !ok {
		return nil, dbus.NewError("org.freedesktop.systemd1.NoSuchUnit", []interface{}{"unit not found"})
	}

	switch iface {
	case "org.freedesktop.systemd1.Unit":
		state := "active"
		if unit.State == "exited" {
			state = "inactive"
		} else if unit.State == "failed" {
			state = "failed"
		}
		return map[string]dbus.Variant{
			"Id":                   dbus.MakeVariant(u.name),
			"Description":          dbus.MakeVariant(unit.Description),
			"ActiveState":          dbus.MakeVariant(state),
			"ActiveEnterTimestamp": dbus.MakeVariant(uint64(unit.StartedAt.UnixMicro())),
		}, nil
	case "org.freedesktop.systemd1.Service":
		return map[string]dbus.Variant{
			"MainPID":          dbus.MakeVariant(uint32(unit.PID)),
			"WorkingDirectory": dbus.MakeVariant(unit.WorkingDir),
			"ExecMainStatus":   dbus.MakeVariant(int32(unit.ExitStatus)),
		}, nil
	}

	return nil, dbus.NewError("org.freedesktop.DBus.Error.UnknownInterface", []interface{}{iface})
}

// Set implements org.freedesktop.DBus.Properties.Set (not supported)
func (u *UnitObject) Set(iface, prop string, val dbus.Variant) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.PropertyReadOnly", nil)
}

// Ref increments the reference count to prevent garbage collection.
// In mini-systemd we don't actually GC units, so this is a no-op.
func (u *UnitObject) Ref() *dbus.Error {
	return nil
}

// Unref decrements the reference count.
func (u *UnitObject) Unref() *dbus.Error {
	return nil
}

// NewManager creates a new process manager
func NewManager(conn *dbus.Conn, journal *JournalService) *Manager {
	return &Manager{
		units:   make(map[string]*Unit),
		conn:    conn,
		journal: journal,
	}
}

// emitJobRemoved sends the JobRemoved signal that go-systemd waits for
func (m *Manager) emitJobRemoved(jobID uint32, jobPath dbus.ObjectPath, unitName string, result string) {
	m.conn.Emit("/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager.JobRemoved",
		jobID, jobPath, unitName, result)
}

// emitPropertiesChanged sends the PropertiesChanged signal for a unit
func (m *Manager) emitPropertiesChanged(unitName string, changedProps map[string]dbus.Variant) {
	unitPath := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/systemd1/unit/%s", escapeUnitNameForPath(unitName)))
	m.conn.Emit(unitPath, "org.freedesktop.DBus.Properties.PropertiesChanged",
		"org.freedesktop.systemd1.Unit", changedProps, []string{})
}

// escapeUnitNameForPath escapes a unit name for use in D-Bus object paths
func escapeUnitNameForPath(name string) string {
	var result []byte
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			result = append(result, c)
		} else {
			result = append(result, '_')
			result = append(result, "0123456789abcdef"[c>>4])
			result = append(result, "0123456789abcdef"[c&0xf])
		}
	}
	return string(result)
}

// Property matches systemd's D-Bus property type (sv)
type Property struct {
	Name  string
	Value dbus.Variant
}

// PropertyCollection matches systemd's aux properties type (sa(sv))
type PropertyCollection struct {
	Name       string
	Properties []Property
}

// StartTransientUnit creates and starts a new transient unit (D-Bus method)
// Signature: StartTransientUnit(name string, mode string, properties []Property, aux []PropertyCollection) -> (job ObjectPath)
func (m *Manager) StartTransientUnit(name string, mode string, properties []Property, aux []PropertyCollection) (dbus.ObjectPath, *dbus.Error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	unit := &Unit{
		Name:  name,
		State: "running",
	}

	// FD passing support
	var stdinFD, stdoutFD, stderrFD *int

	// Parse properties
	for _, prop := range properties {
		switch prop.Name {
		case "Description":
			if s, ok := prop.Value.Value().(string); ok {
				unit.Description = s
			}
		case "WorkingDirectory":
			if s, ok := prop.Value.Value().(string); ok {
				unit.WorkingDir = s
			}
		case "BusName":
			if s, ok := prop.Value.Value().(string); ok {
				unit.BusName = s
			}
		case "Environment":
			if envList, ok := prop.Value.Value().([]string); ok {
				unit.Environment = envList
			}
		case "Slice":
			if s, ok := prop.Value.Value().(string); ok {
				unit.Slice = s
			}
		case "ExecStart":
			// ExecStart is a(sasb) - array of (path, argv, ignore-failure)
			val := prop.Value.Value()
			if execList, ok := val.([][]interface{}); ok && len(execList) > 0 {
				exec0 := execList[0]
				if len(exec0) >= 2 {
					if argv, ok := exec0[1].([]string); ok {
						unit.Command = argv
					}
				}
			}
		case "StandardInputFileDescriptor":
			// UnixFD comes as dbus.UnixFDIndex which gets resolved to int by godbus
			if fd, ok := prop.Value.Value().(dbus.UnixFD); ok {
				fdInt := int(fd)
				stdinFD = &fdInt
			}
		case "StandardOutputFileDescriptor":
			if fd, ok := prop.Value.Value().(dbus.UnixFD); ok {
				fdInt := int(fd)
				stdoutFD = &fdInt
			}
		case "StandardErrorFileDescriptor":
			if fd, ok := prop.Value.Value().(dbus.UnixFD); ok {
				fdInt := int(fd)
				stderrFD = &fdInt
			}
		case "BindsTo":
			if units, ok := prop.Value.Value().([]string); ok {
				unit.BindsTo = units
			}
		case "After":
			if units, ok := prop.Value.Value().([]string); ok {
				unit.After = units
			}
		case "ExecStopPost":
			// ExecStopPost is a(sasb) - array of (path, argv, ignore-failure)
			val := prop.Value.Value()
			if execList, ok := val.([][]interface{}); ok {
				for _, exec0 := range execList {
					if len(exec0) >= 2 {
						if argv, ok := exec0[1].([]string); ok {
							unit.ExecStopPost = append(unit.ExecStopPost, argv)
						}
					}
				}
			}
		}
	}

	if len(unit.Command) == 0 {
		return "", dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", []interface{}{"no ExecStart provided"})
	}

	// Start the process
	cmd := exec.Command(unit.Command[0], unit.Command[1:]...)
	cmd.Dir = unit.WorkingDir
	cmd.Env = append(os.Environ(), unit.Environment...)

	// Handle stdio - use passed FDs or create pipes for capture
	var stdoutPipe, stderrPipe interface{ Read([]byte) (int, error) }
	var stdinFile, stdoutFile, stderrFile *os.File

	if stdinFD != nil {
		stdinFile = os.NewFile(uintptr(*stdinFD), "stdin")
		cmd.Stdin = stdinFile
	}

	if stdoutFD != nil {
		stdoutFile = os.NewFile(uintptr(*stdoutFD), "stdout")
		cmd.Stdout = stdoutFile
	} else {
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return "", dbus.NewError("org.freedesktop.DBus.Error.Failed", []interface{}{err.Error()})
		}
		stdoutPipe = stdout
	}

	if stderrFD != nil {
		stderrFile = os.NewFile(uintptr(*stderrFD), "stderr")
		cmd.Stderr = stderrFile
	} else {
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return "", dbus.NewError("org.freedesktop.DBus.Error.Failed", []interface{}{err.Error()})
		}
		stderrPipe = stderr
	}

	if err := cmd.Start(); err != nil {
		return "", dbus.NewError("org.freedesktop.DBus.Error.Failed", []interface{}{err.Error()})
	}

	// Close our copies of passed FDs - child has inherited them.
	// This is critical for PTYs: the master won't get EOF until all
	// copies of the slave fd are closed.
	if stdinFile != nil {
		stdinFile.Close()
	}
	if stdoutFile != nil {
		stdoutFile.Close()
	}
	if stderrFile != nil {
		stderrFile.Close()
	}

	unit.Cmd = cmd
	unit.PID = cmd.Process.Pid
	unit.StartedAt = time.Now()

	m.units[name] = unit

	// Export the unit object on D-Bus for Properties and Unit interfaces
	unitPath := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/systemd1/unit/%s", escapeUnitNameForPath(name)))
	unitObj := &UnitObject{manager: m, name: name}
	m.conn.Export(unitObj, unitPath, "org.freedesktop.DBus.Properties")
	m.conn.Export(unitObj, unitPath, "org.freedesktop.systemd1.Unit")

	// Capture output in background (only for pipes we created)
	if stdoutPipe != nil {
		go m.captureOutput(name, stdoutPipe, "stdout")
	}
	if stderrPipe != nil {
		go m.captureOutput(name, stderrPipe, "stderr")
	}

	// Monitor process exit
	go m.waitForExit(name)

	// Generate job ID and path
	m.jobID++
	jobID := m.jobID
	jobPath := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/systemd1/job/%d", jobID))

	// Emit JobRemoved signal immediately (job "done" = unit started)
	// go-systemd waits for this signal before returning from StartTransientUnit
	go func() {
		time.Sleep(10 * time.Millisecond) // Small delay to ensure client is listening
		m.emitJobRemoved(jobID, jobPath, name, "done")
	}()

	return jobPath, nil
}

// StopUnit stops a unit (D-Bus method)
func (m *Manager) StopUnit(name string, mode string) (dbus.ObjectPath, *dbus.Error) {
	m.mu.Lock()
	unit, ok := m.units[name]
	m.mu.Unlock()

	if !ok {
		return "", dbus.NewError("org.freedesktop.DBus.Error.UnknownObject", []interface{}{"unit not found"})
	}

	if unit.Cmd != nil && unit.Cmd.Process != nil {
		unit.Cmd.Process.Signal(syscall.SIGTERM)
	}

	jobPath := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/systemd1/job/%d", time.Now().UnixNano()))
	return jobPath, nil
}

// KillUnit sends a signal to a unit (D-Bus method)
func (m *Manager) KillUnit(name string, who string, signal int32) *dbus.Error {
	m.mu.Lock()
	unit, ok := m.units[name]
	m.mu.Unlock()

	if !ok {
		return dbus.NewError("org.freedesktop.DBus.Error.UnknownObject", []interface{}{"unit not found"})
	}

	if unit.Cmd != nil && unit.Cmd.Process != nil {
		unit.Cmd.Process.Signal(syscall.Signal(signal))
	}

	return nil
}

// ListUnitsByPatterns returns units matching patterns (D-Bus method)
func (m *Manager) ListUnitsByPatterns(states []string, patterns []string) ([][]interface{}, *dbus.Error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result [][]interface{}
	for name, unit := range m.units {
		// Check pattern match
		matched := false
		for _, pattern := range patterns {
			if match, _ := filepath.Match(pattern, name); match {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}

		// Check state match
		stateMatched := len(states) == 0
		for _, s := range states {
			if s == unit.State || s == "active" && unit.State == "running" {
				stateMatched = true
				break
			}
		}
		if !stateMatched {
			continue
		}

		// Return in systemd's format: (ssssssouso)
		result = append(result, []interface{}{
			name,             // name
			unit.Description, // description
			"loaded",         // load state
			unit.State,       // active state
			"",               // sub state
			"",               // following
			dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/systemd1/unit/%s", strings.ReplaceAll(name, ".", "_"))),
			uint32(0),            // job id
			"",                   // job type
			dbus.ObjectPath("/"), // job path
		})
	}

	return result, nil
}

// GetUnit returns unit path (D-Bus method)
func (m *Manager) GetUnit(name string) (dbus.ObjectPath, *dbus.Error) {
	m.mu.RLock()
	_, ok := m.units[name]
	m.mu.RUnlock()

	if !ok {
		return "", dbus.NewError("org.freedesktop.systemd1.NoSuchUnit", []interface{}{"unit not found"})
	}

	path := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/systemd1/unit/%s", strings.ReplaceAll(name, ".", "_")))
	return path, nil
}

// GetUnitProperties returns properties for a unit
func (m *Manager) GetUnitProperties(name string) (map[string]dbus.Variant, *dbus.Error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	unit, ok := m.units[name]
	if !ok {
		return nil, dbus.NewError("org.freedesktop.systemd1.NoSuchUnit", []interface{}{"unit not found"})
	}

	activeState := "active"
	if unit.State != "running" {
		activeState = "inactive"
	}

	return map[string]dbus.Variant{
		"Id":                   dbus.MakeVariant(name),
		"Description":          dbus.MakeVariant(unit.Description),
		"ActiveState":          dbus.MakeVariant(activeState),
		"ActiveEnterTimestamp": dbus.MakeVariant(uint64(unit.StartedAt.UnixMicro())),
	}, nil
}

// GetServiceProperties returns service-specific properties
func (m *Manager) GetServiceProperties(name string) (map[string]dbus.Variant, *dbus.Error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	unit, ok := m.units[name]
	if !ok {
		return nil, dbus.NewError("org.freedesktop.systemd1.NoSuchUnit", []interface{}{"unit not found"})
	}

	slog.Debug("GetServiceProperties", "unit", name, "state", unit.State, "exitStatus", unit.ExitStatus)
	return map[string]dbus.Variant{
		"MainPID":          dbus.MakeVariant(uint32(unit.PID)),
		"WorkingDirectory": dbus.MakeVariant(unit.WorkingDir),
		"ExecMainStatus":   dbus.MakeVariant(int32(unit.ExitStatus)),
	}, nil
}

// ReadLogs returns logs for a unit (custom method on sh.swa.MiniSystemd.Logs)
func (m *Manager) ReadLogs(unitName string, cursor int64) ([][]interface{}, int64, *dbus.Error) {
	m.mu.RLock()
	unit, ok := m.units[unitName]
	m.mu.RUnlock()

	if !ok {
		return nil, 0, dbus.NewError("org.freedesktop.DBus.Error.UnknownObject", []interface{}{"unit not found"})
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	var logs [][]interface{}
	allLogs := append(unit.Stdout, unit.Stderr...)

	// Sort by timestamp would be nice, but for simplicity just concat
	start := int(cursor)
	if start < 0 {
		start = 0
	}
	if start >= len(allLogs) {
		return logs, int64(len(allLogs)), nil
	}

	for i := start; i < len(allLogs); i++ {
		entry := allLogs[i]
		logs = append(logs, []interface{}{
			entry.Timestamp.UnixMicro(),
			entry.Stream,
			entry.Data,
		})
	}

	return logs, int64(len(allLogs)), nil
}

// captureOutput reads from a pipe and stores log entries
func (m *Manager) captureOutput(unitName string, pipe interface{ Read([]byte) (int, error) }, stream string) {
	buf := make([]byte, 4096)
	for {
		n, err := pipe.Read(buf)
		if n > 0 {
			data := string(buf[:n])
			m.mu.Lock()
			var sliceName string
			if unit, ok := m.units[unitName]; ok {
				entry := LogEntry{
					Timestamp: time.Now(),
					Stream:    stream,
					Data:      data,
				}
				if stream == "stdout" {
					unit.Stdout = append(unit.Stdout, entry)
				} else {
					unit.Stderr = append(unit.Stderr, entry)
				}
				sliceName = unit.Slice
			}
			m.mu.Unlock()

			// Also write to journal service for querying
			if m.journal != nil {
				m.journal.AddFromUnit(unitName, sliceName, stream, data)
			}
		}
		if err != nil {
			break
		}
	}
}

// waitForExit monitors a process and updates its state
func (m *Manager) waitForExit(unitName string) {
	m.mu.RLock()
	unit, ok := m.units[unitName]
	m.mu.RUnlock()

	if !ok || unit.Cmd == nil {
		return
	}

	err := unit.Cmd.Wait()

	m.mu.Lock()
	var activeState string
	var serviceResult string
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			unit.ExitStatus = exitErr.ExitCode()
			unit.State = "failed"
			activeState = "failed"
			serviceResult = "exit-code"
			slog.Debug("process exited", "unit", unitName, "exitCode", exitErr.ExitCode(), "state", unit.State)
		} else {
			unit.State = "failed"
			activeState = "failed"
			serviceResult = "failed"
			slog.Debug("process failed", "unit", unitName, "error", err, "state", unit.State)
		}
	} else {
		unit.ExitStatus = 0
		unit.State = "exited"
		activeState = "inactive"
		serviceResult = "success"
		slog.Debug("process exited", "unit", unitName, "exitCode", 0, "state", unit.State)
	}

	// Copy ExecStopPost before unlocking
	execStopPost := unit.ExecStopPost
	exitStatus := unit.ExitStatus
	m.mu.Unlock()

	// Run ExecStopPost commands with specifier substitution (like real systemd)
	exitStatusStr := fmt.Sprintf("%d", exitStatus)
	for _, cmd := range execStopPost {
		if len(cmd) == 0 {
			continue
		}
		// Substitute $EXIT_STATUS and $SERVICE_RESULT in arguments
		args := make([]string, len(cmd))
		for i, arg := range cmd {
			arg = strings.ReplaceAll(arg, "$EXIT_STATUS", exitStatusStr)
			arg = strings.ReplaceAll(arg, "$SERVICE_RESULT", serviceResult)
			args[i] = arg
		}
		slog.Debug("running ExecStopPost", "unit", unitName, "cmd", args)
		execCmd := exec.Command(args[0], args[1:]...)
		execCmd.Env = os.Environ()
		if err := execCmd.Run(); err != nil {
			slog.Debug("ExecStopPost failed", "unit", unitName, "cmd", args, "error", err)
		}
	}

	slog.Debug("emitting PropertiesChanged", "unit", unitName, "activeState", activeState)
	// Emit PropertiesChanged signal so watchers know the unit exited
	m.emitPropertiesChanged(unitName, map[string]dbus.Variant{
		"ActiveState": dbus.MakeVariant(activeState),
	})

	// Handle BindsTo: stop units that depend on this one
	m.stopDependentUnits(unitName)
}

// stopDependentUnits stops all units that have BindsTo pointing to the given unit
func (m *Manager) stopDependentUnits(stoppedUnit string) {
	m.mu.RLock()
	var dependents []string
	for name, unit := range m.units {
		if unit.State == "running" {
			for _, dep := range unit.BindsTo {
				if dep == stoppedUnit {
					dependents = append(dependents, name)
					break
				}
			}
		}
	}
	m.mu.RUnlock()

	// Stop each dependent unit
	for _, name := range dependents {
		slog.Debug("stopping dependent unit due to BindsTo", "unit", name, "dependsOn", stoppedUnit)
		m.StopUnit(name, "replace")
	}
}
