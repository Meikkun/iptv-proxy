package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestPlaylistSIGHUPRefreshAndShutdown(t *testing.T) {
	if os.Getenv("IPTV_CATALOGUE_SIGNAL_HELPER") == "1" {
		conf := catalogueConfig(os.Getenv("IPTV_CATALOGUE_TEST_SOURCE"))
		conf.HostConfig.Port, _ = strconv.Atoi(os.Getenv("IPTV_CATALOGUE_TEST_PORT"))
		c, err := NewServer(conf)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Serve(); err != nil {
			t.Fatal(err)
		}
		return
	}
	var calls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		fmt.Fprintf(w, "#EXTM3U\n#EXTINF:-1,Version%d\nhttp://provider.example/live/u/p/1.ts\n", n)
	}))
	defer upstream.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPlaylistSIGHUPRefreshAndShutdown$")
	cmd.Env = append(os.Environ(), "IPTV_CATALOGUE_SIGNAL_HELPER=1",
		"IPTV_CATALOGUE_TEST_SOURCE="+upstream.URL, fmt.Sprintf("IPTV_CATALOGUE_TEST_PORT=%d", port))
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	statusURL := fmt.Sprintf("http://127.0.0.1:%d/status", port)
	generation := func() uint64 {
		resp, err := client.Get(statusURL)
		if err != nil {
			return 0
		}
		defer resp.Body.Close()
		var status statusResponse
		if json.NewDecoder(resp.Body).Decode(&status) != nil || status.Catalogue == nil {
			return 0
		}
		return status.Catalogue.Generation
	}
	eventually(t, func() bool { return generation() == 1 })
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return generation() == 2 })
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatal("SIGHUP generated more than one refresh")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		stopped = true
		if err != nil {
			t.Fatalf("signal helper failed: %v\n%s", err, output.String())
		}
	case <-time.After(3 * time.Second):
		cmd.Process.Kill()
		<-done
		stopped = true
		t.Fatal("SIGTERM did not stop server")
	}
}
