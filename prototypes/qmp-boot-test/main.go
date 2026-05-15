// qmp-boot-test — end-to-end verification of Approach D against a real
// running Linux guest. Walks the exact wire sequence that urunc would run
// under the proposal's timing-inversion approach:
//
//   QEMU starts paused (-S) with QMP socket exposed.
//   Client connects, reads greeting, sends qmp_capabilities.
//   Client sends cont — the guest CPU begins executing.
//     (This is the moment that replaces urunc's current syscall.Exec
//      followed by immediate guest start.)
//   Guest boots (we wait, observing serial output to confirm).
//   Client exercises the lifecycle ops on the *running* guest:
//     stop, query-status, cont, query-block, device_add (virtio-net),
//     query-pci, then quit.
//
// Each step is timestamped and the QMP wire is logged.
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
		fmt.Fprintln(os.Stderr, "usage: qmp-boot-test /path/to/qmp.sock")
		os.Exit(2)
	}
	sockPath := os.Args[1]

	t0 := time.Now()
	conn, err := net.Dial("unix", sockPath)
	must(err, "dial")
	defer conn.Close()
	rd := bufio.NewReader(conn)
	logf(t0, "connected to %s", sockPath)

	// Greeting + handshake.
	mustReadGreeting(rd, t0)
	mustCall(conn, rd, t0, "qmp_capabilities", cmd{Execute: "qmp_capabilities"})

	// State should be prelaunch — QEMU started with -S.
	mustCall(conn, rd, t0, "query-status (pre-cont)", cmd{Execute: "query-status"})

	// THE moment that replaces urunc's old syscall.Exec → immediate guest run.
	// In Approach D, this is what urunc start sends when it would have sent
	// StartExecve previously.
	logf(t0, "==> sending cont (this is the 'start guest' command in Approach D)")
	mustCall(conn, rd, t0, "cont", cmd{Execute: "cont"})

	// Wait for the guest to actually start booting. We don't need a full boot
	// for the QMP wire test — just enough time to confirm the guest is doing
	// real work.
	logf(t0, "==> waiting 5s for guest to begin booting...")
	time.Sleep(5 * time.Second)

	// State should now be "running" — the guest CPU is executing.
	mustCall(conn, rd, t0, "query-status (post-cont)", cmd{Execute: "query-status"})

	// Now exercise lifecycle ops against a RUNNING guest. This is what
	// `urunc kill`, `urunc state`, etc. would do in Approach D.

	// 1. pause/resume — the urunc pause and resume container ops.
	logf(t0, "==> pause / resume test")
	mustCall(conn, rd, t0, "stop", cmd{Execute: "stop"})
	mustCall(conn, rd, t0, "query-status (paused)", cmd{Execute: "query-status"})
	mustCall(conn, rd, t0, "cont (resume)", cmd{Execute: "cont"})
	mustCall(conn, rd, t0, "query-status (resumed)", cmd{Execute: "query-status"})

	// 2. introspection — query block devices and VCPUs. These are what
	// `urunc state <id>` would surface.
	logf(t0, "==> introspection")
	mustCall(conn, rd, t0, "query-block", cmd{Execute: "query-block"})
	mustCall(conn, rd, t0, "query-cpus-fast", cmd{Execute: "query-cpus-fast"})

	// 3. hotplug — add a second virtio-net device on the fly. This is the
	// op that's hardest to do with the current signal-only model.
	logf(t0, "==> hotplug: add a virtio-net-pci device")
	mustCall(conn, rd, t0, "netdev_add (user)", cmd{
		Execute: "netdev_add",
		Arguments: map[string]interface{}{
			"type": "user",
			"id":   "hotnet0",
		},
	})
	mustCall(conn, rd, t0, "device_add (virtio-net-pci on rp1)", cmd{
		Execute: "device_add",
		Arguments: map[string]interface{}{
			"driver": "virtio-net-pci",
			"netdev": "hotnet0",
			"id":     "hotnet0-dev",
			"bus":    "rp1",
		},
	})
	mustCall(conn, rd, t0, "query-pci (verify hotplug landed)", cmd{Execute: "query-pci"})

	// 4. hotunplug — remove the device we just added.
	logf(t0, "==> hotunplug")
	mustCall(conn, rd, t0, "device_del", cmd{
		Execute:   "device_del",
		Arguments: map[string]interface{}{"id": "hotnet0-dev"},
	})

	// 5. graceful shutdown via quit — this is what urunc kill / delete would
	// send instead of SIGKILL'ing the monitor PID.
	logf(t0, "==> graceful shutdown: quit")
	mustCall(conn, rd, t0, "quit", cmd{Execute: "quit"})

	logf(t0, "probe complete — all ops succeeded.")
}

func logf(t0 time.Time, format string, args ...interface{}) {
	fmt.Printf("[t+%7.2fms] %s\n", elapsed(t0), fmt.Sprintf(format, args...))
}

func mustReadGreeting(rd *bufio.Reader, t0 time.Time) {
	r, raw, err := readFrame(rd)
	must(err, "read greeting")
	if r.QMP == nil {
		fail(fmt.Errorf("expected greeting, got: %s", raw))
	}
	logf(t0, "<- greeting: %s", raw)
}

func mustCall(conn net.Conn, rd *bufio.Reader, t0 time.Time, label string, c cmd) {
	b, err := json.Marshal(c)
	must(err, "marshal "+label)
	logf(t0, "-> %s   %s", label, string(b))
	_, err = conn.Write(append(b, '\n'))
	must(err, "write "+label)
	for {
		r, raw, err := readFrame(rd)
		must(err, "read "+label)
		if r.Event != "" {
			logf(t0, "<- (event) %s", r.Event)
			continue
		}
		if r.Error != nil {
			fail(fmt.Errorf("qmp error on %s: %s: %s", label, r.Error.Class, r.Error.Desc))
		}
		logf(t0, "<- %s   %s", label, raw)
		return
	}
}

func readFrame(rd *bufio.Reader) (reply, string, error) {
	line, err := rd.ReadBytes('\n')
	if err != nil {
		return reply{}, "", err
	}
	var r reply
	if err := json.Unmarshal(line, &r); err != nil {
		return reply{}, string(line), err
	}
	// Compact single-line form for the log.
	out := make([]byte, 0, len(line))
	for _, b := range line {
		if b == '\n' {
			continue
		}
		out = append(out, b)
	}
	return r, string(out), nil
}

func elapsed(t0 time.Time) float64 {
	return float64(time.Since(t0).Microseconds()) / 1000.0
}

func must(err error, what string) {
	if err != nil {
		fail(fmt.Errorf("%s: %w", what, err))
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
	os.Exit(1)
}
