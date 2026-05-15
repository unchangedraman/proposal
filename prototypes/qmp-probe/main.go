// QMP probe — a minimal demonstration of driving a QEMU monitor over its
// QEMU Machine Protocol (QMP) Unix socket. Written as the LFX 2026 Term 2
// pre-application exercise for urunc issue #112.
//
// Usage: ./qmp-probe /path/to/qmp.sock
//
// What it does, in order:
//   1. Connects to the QMP Unix socket QEMU exposed via -qmp unix:<path>,server,nowait.
//   2. Reads the "greeting" frame QEMU sends on connect (contains QEMU version + capabilities).
//   3. Sends qmp_capabilities (required handshake before any other command).
//   4. Sends query-status (probe the VM state machine).
//   5. Sends cont (start CPU execution — pairs with QEMU's -S flag).
//   6. Sends query-status again (verify the state transitioned).
//   7. Sends query-vcpus (introspection).
//   8. Sends quit (graceful shutdown — replaces signal-based kill).
//
// Each command is one JSON object per line on the wire (line-delimited JSON).
// Replies are one JSON object per line back. There are no streaming events
// in this probe; QMP events are a separate exercise.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

type cmd struct {
	Execute   string                 `json:"execute"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

// reply covers every QMP frame shape urunc needs:
//   - greeting:     {"QMP": {...}}
//   - command ok:   {"return": <anything — object, array, or even null>}
//   - command err:  {"error": {"class": "...", "desc": "..."}}
//   - async event:  {"event": "RESUME", "timestamp": {...}, "data": {...}}
//
// Note Return is json.RawMessage rather than a typed map: query-status returns
// an object, query-cpus-fast returns an array, quit returns null. A single
// shared client can't statically type the reply; callers parse per-command.
type reply struct {
	Return json.RawMessage `json:"return,omitempty"`
	Error  *struct {
		Class string `json:"class"`
		Desc  string `json:"desc"`
	} `json:"error,omitempty"`
	Event     string                 `json:"event,omitempty"`
	Timestamp map[string]interface{} `json:"timestamp,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
	QMP       map[string]interface{} `json:"QMP,omitempty"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: qmp-probe /path/to/qmp.sock")
		os.Exit(2)
	}
	sockPath := os.Args[1]

	t0 := time.Now()
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial %s: %v\n", sockPath, err)
		os.Exit(1)
	}
	defer conn.Close()
	fmt.Printf("[t+%6.2fms] connected to %s\n", elapsed(t0), sockPath)

	rd := bufio.NewReader(conn)

	// Step 1: read the greeting QEMU sends on connect.
	if err := readGreeting(rd, t0); err != nil {
		fail(err)
	}

	// Step 2: handshake. Until qmp_capabilities is sent, QEMU rejects everything else.
	if err := sendAndRead(conn, rd, t0, "qmp_capabilities", cmd{Execute: "qmp_capabilities"}); err != nil {
		fail(err)
	}

	// Step 3: probe the VM state. With -S, this should be "paused" / "prelaunch".
	if err := sendAndRead(conn, rd, t0, "query-status (before cont)", cmd{Execute: "query-status"}); err != nil {
		fail(err)
	}

	// Step 4: cont — start CPU execution.
	if err := sendAndRead(conn, rd, t0, "cont", cmd{Execute: "cont"}); err != nil {
		fail(err)
	}

	// Step 5: probe again. Should now be "running".
	if err := sendAndRead(conn, rd, t0, "query-status (after cont)", cmd{Execute: "query-status"}); err != nil {
		fail(err)
	}

	// Step 6: introspection on the VCPUs.
	if err := sendAndRead(conn, rd, t0, "query-cpus-fast", cmd{Execute: "query-cpus-fast"}); err != nil {
		fail(err)
	}

	// Step 7: stop — pause CPU execution. Pairs with cont above.
	if err := sendAndRead(conn, rd, t0, "stop", cmd{Execute: "stop"}); err != nil {
		fail(err)
	}

	// Step 8: graceful shutdown via quit. This is the bit that replaces
	// urunc's current `killProcess` call in qemu.go:Stop().
	if err := sendAndRead(conn, rd, t0, "quit", cmd{Execute: "quit"}); err != nil {
		fail(err)
	}

	fmt.Printf("[t+%6.2fms] probe complete.\n", elapsed(t0))
}

// sendAndRead writes a QMP command and then reads frames from the socket
// until we get the actual reply to *this* command — skipping past any
// async events that arrived in the meantime.
//
// QMP gotcha learned the hard way: events (e.g. RESUME, STOP) arrive on the
// same socket as command replies, in arrival order. A naive "one read per
// command" client shifts by one frame the moment any command triggers an
// event. A correct client must type-dispatch each frame: a frame with
// "event" is async telemetry; a frame with "return" or "error" is the
// reply to the most recent in-flight command.
func sendAndRead(conn net.Conn, rd *bufio.Reader, t0 time.Time, label string, c cmd) error {
	b, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", label, err)
	}
	fmt.Printf("[t+%6.2fms] -> %s\n", elapsed(t0), string(b))
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", label, err)
	}
	for {
		r, raw, err := readFrame(rd)
		if err != nil {
			return fmt.Errorf("read %s: %w", label, err)
		}
		if r.Event != "" {
			fmt.Printf("[t+%6.2fms] <- (async event) %s\n", elapsed(t0), r.Event)
			continue // not the reply we want — keep reading
		}
		if r.Error != nil {
			fmt.Printf("[t+%6.2fms] <- %s (ERROR): %s: %s\n", elapsed(t0), label, r.Error.Class, r.Error.Desc)
			return fmt.Errorf("qmp error on %s: %s", label, r.Error.Desc)
		}
		// Got the reply.
		fmt.Printf("[t+%6.2fms] <- %s\n%s\n", elapsed(t0), label, string(raw))
		return nil
	}
}

// readGreeting reads the one-shot frame QEMU sends on connect.
func readGreeting(rd *bufio.Reader, t0 time.Time) error {
	r, raw, err := readFrame(rd)
	if err != nil {
		return err
	}
	if r.QMP == nil {
		return fmt.Errorf("expected greeting, got: %s", raw)
	}
	fmt.Printf("[t+%6.2fms] <- greeting\n%s\n", elapsed(t0), raw)
	return nil
}

// readFrame reads a single newline-delimited JSON frame from QMP and
// returns it both parsed and as the pretty-printed original.
func readFrame(rd *bufio.Reader) (reply, string, error) {
	line, err := rd.ReadBytes('\n')
	if err != nil {
		return reply{}, "", err
	}
	var r reply
	if err := json.Unmarshal(line, &r); err != nil {
		return reply{}, string(line), err
	}
	pretty, _ := json.MarshalIndent(r, "", "  ")
	return r, string(pretty), nil
}

func elapsed(t0 time.Time) float64 {
	return float64(time.Since(t0).Microseconds()) / 1000.0
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
	os.Exit(1)
}
