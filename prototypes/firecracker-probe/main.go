// fc-probe — drive a real Firecracker microVM through its HTTP-over-Unix-socket
// API end-to-end. Companion exercise to qmp-probe; together these satisfy
// cmainas's pre-application requirement in urunc#112 to "create a VM
// configuring it through the monitor's API."
//
// Firecracker's API is documented at:
//   https://github.com/firecracker-microvm/firecracker/blob/main/src/firecracker/swagger/firecracker.yaml
//
// Flow this probe drives:
//   GET /                         (sanity: read Instance Info)
//   PUT /machine-config           (set vCPUs + memory)
//   PUT /boot-source              (kernel + boot args)
//   PUT /drives/rootfs            (rootfs block device)
//   PUT /actions {InstanceStart}  (boot the guest)
//   GET /                         (verify state == Running)
//   PATCH /vm {state: Paused}     (pause CPU — equivalent of QMP "stop")
//   PATCH /vm {state: Resumed}    (resume — equivalent of QMP "cont")
//   PUT /actions {SendCtrlAltDel} (graceful shutdown signal)
//
// Each call prints {method URL -> status} and the response body if any.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: fc-probe /path/to/api.sock /path/to/vmlinux /path/to/rootfs.ext4")
		os.Exit(2)
	}
	sock, kernel, rootfs := os.Args[1], os.Args[2], os.Args[3]

	t0 := time.Now()
	c := newClient(sock)

	// 1. Sanity check — instance info should respond with Uninitialized state.
	must(c.call(t0, "GET", "/", nil))

	// 2. Configure the machine: 1 vCPU, 128 MiB. Firecracker insists on this
	//    being set *before* PUT /boot-source.
	must(c.call(t0, "PUT", "/machine-config", map[string]any{
		"vcpu_count":  1,
		"mem_size_mib": 128,
	}))

	// 3. Boot source — kernel + cmdline. The "console=ttyS0 reboot=k panic=1"
	//    args are the FC quickstart default.
	must(c.call(t0, "PUT", "/boot-source", map[string]any{
		"kernel_image_path": kernel,
		"boot_args":         "console=ttyS0 reboot=k panic=1 pci=off",
	}))

	// 4. Root filesystem. is_root_device=true tells the guest to mount this as /
	//    via virtio-blk. ID "rootfs" is conventional.
	must(c.call(t0, "PUT", "/drives/rootfs", map[string]any{
		"drive_id":       "rootfs",
		"path_on_host":   rootfs,
		"is_root_device": true,
		"is_read_only":   true,
	}))

	// 5. Boot the VM. This is the equivalent of QMP's `cont` after `-S`:
	//    everything above is preparation; this is the "go" command.
	must(c.call(t0, "PUT", "/actions", map[string]any{
		"action_type": "InstanceStart",
	}))

	// Give the guest a moment to settle.
	time.Sleep(800 * time.Millisecond)

	// 6. Check state — should be Running now.
	must(c.call(t0, "GET", "/", nil))

	// 7. Pause via PATCH /vm. FC exposes Pause/Resume at the VM resource,
	//    not as actions. QMP equivalent: "stop".
	must(c.call(t0, "PATCH", "/vm", map[string]any{
		"state": "Paused",
	}))

	// 8. Resume. QMP equivalent: "cont".
	must(c.call(t0, "PATCH", "/vm", map[string]any{
		"state": "Resumed",
	}))

	// 9. Graceful shutdown. SendCtrlAltDel is how FC asks the guest to reboot,
	//    which on most distros maps to systemd-shutdown. There is no direct
	//    "quit and unwind the monitor" in FC v1 — the host kills the process
	//    after the guest stops. urunc's MonitorControl Stop() needs to know this.
	must(c.call(t0, "PUT", "/actions", map[string]any{
		"action_type": "SendCtrlAltDel",
	}))

	fmt.Printf("[t+%6.2fms] probe complete.\n", elapsed(t0))
}

type client struct {
	http *http.Client
}

func newClient(sock string) *client {
	return &client{
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", sock)
				},
			},
			Timeout: 5 * time.Second,
		},
	}
}

// call sends a JSON request to the Firecracker API and prints the result.
// "unix" + http.NoBody is how Go's net/http drives Unix-socket HTTP cleanly.
func (c *client) call(t0 time.Time, method, path string, body map[string]any) error {
	var reader io.Reader
	var bodyStr string
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
		bodyStr = " " + string(b)
	}
	url := "http://unix" + path
	fmt.Printf("[t+%6.2fms] -> %s %s%s\n", elapsed(t0), method, path, bodyStr)
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	out = bytes.TrimSpace(out)
	if len(out) > 0 {
		var pretty bytes.Buffer
		if json.Indent(&pretty, out, "", "  ") == nil {
			out = pretty.Bytes()
		}
		fmt.Printf("[t+%6.2fms] <- %d %s\n%s\n", elapsed(t0), resp.StatusCode, http.StatusText(resp.StatusCode), out)
	} else {
		fmt.Printf("[t+%6.2fms] <- %d %s (empty body)\n", elapsed(t0), resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s %s -> %d", method, path, resp.StatusCode)
	}
	return nil
}

func elapsed(t0 time.Time) float64 {
	return float64(time.Since(t0).Microseconds()) / 1000.0
}

func must(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}
