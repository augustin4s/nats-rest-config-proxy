package test

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats-rest-config-proxy/internal/server"
	gnatsd "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
)

func waitServerIsReady(t *testing.T, ctx context.Context, host string) {
	for range time.NewTicker(50 * time.Millisecond).C {
		select {
		case <-ctx.Done():
			if ctx.Err() == context.Canceled {
				t.Fatal(ctx.Err())
			}
		default:
		}

		resp, err := http.Get(fmt.Sprintf("%s/healthz", host))
		if err != nil {
			t.Logf("Error: %s", err)
			continue
		}
		if resp != nil && resp.StatusCode == 200 {
			break
		}
	}
}

func waitServerIsDone(t *testing.T, ctx context.Context, host string) {
	for range time.NewTicker(50 * time.Millisecond).C {
		select {
		case <-ctx.Done():
			if ctx.Err() == context.Canceled {
				t.Fatal(ctx.Err())
			}
		default:
		}

		resp, err := http.Get(fmt.Sprintf("%s/healthz", host))
		if err == nil && resp.StatusCode != 200 {
			continue
		}
		break
	}
}

func curl(method string, endpoint string, payload []byte) (*http.Response, []byte, error) {
	result, err := url.Parse(endpoint)
	if err != nil {
		return nil, nil, err
	}
	e := fmt.Sprintf("%s://%s%s", result.Scheme, result.Host, result.Path)
	buf := bytes.NewBuffer([]byte(payload))
	req, err := http.NewRequest(method, e, buf)
	if err != nil {
		return nil, nil, err
	}
	if len(result.Query()) > 0 {
		for k, v := range result.Query() {
			req.URL.Query().Add(k, string(v[0]))
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	return resp, body, nil
}

var testPort int = 5567

func DefaultOptions() *server.Options {
	opts := &server.Options{
		NoSignals: true,
		NoLog:     true,
		Debug:     true,
		Trace:     true,
		Host:      "localhost",
		Port:      testPort,
		DataDir:   "./data",
	}
	if os.Getenv("DEBUG") == "true" {
		opts.NoLog = false
	}
	testPort += 1

	return opts
}

func TestBasicRunServer(t *testing.T) {
	opts := DefaultOptions()
	opts.Port = 0
	s := server.NewServer(opts)
	ctx, done := context.WithCancel(context.Background())

	time.AfterFunc(100*time.Millisecond, func() {
		done()
	})

	err := s.Run(ctx)
	if err != nil && err != context.Canceled {
		t.Fatalf("Unexpected error running server: %s", err)
	}
}

func TestFullCycle(t *testing.T) {
	// Create a data directory.
	opts := DefaultOptions()
	s := server.NewServer(opts)
	host := fmt.Sprintf("http://%s:%d", opts.Host, opts.Port)
	ctx, _ := context.WithTimeout(context.Background(), 5*time.Second)
	time.AfterFunc(2*time.Second, func() {
		s.Shutdown(ctx)
		waitServerIsDone(t, ctx, host)
	})
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		done <- struct{}{}
	}()
	waitServerIsReady(t, ctx, host)

	// Create a couple of users
	payload := `{
	  "username": "user-a",
	  "password": "secret",
          "permissions": "normal-user"
	}`
	resp, _, err := curl("PUT", host+"/v1/auth/idents/user-a", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("Expected OK, got: %v", resp.StatusCode)
	}

	payload = `{
	  "username": "user-b",
	  "password": "secret",
          "permissions": "normal-user"
	}`
	resp, _, err = curl("PUT", host+"/v1/auth/idents/user-b", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("Expected OK, got: %v", resp.StatusCode)
	}

	// Create the permissions
	payload = `{
         "publish": {
           "allow": ["foo", "bar"]
          },
          "subscribe": {
            "deny": ["quux"]
          }
	}`
	resp, _, err = curl("PUT", host+"/v1/auth/perms/normal-user", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("Expected OK, got: %v", resp.StatusCode)
	}

	// Create a Snapshot
	resp, _, err = curl("POST", host+"/v1/auth/snapshot?name=hello", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("Expected OK, got: %v", resp.StatusCode)
	}

	// Publish a named snapshot
	resp, _, err = curl("POST", host+"/v1/auth/publish?name=hello", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("Expected OK, got: %v", resp.StatusCode)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for server to stop")
	}

	config := fmt.Sprintf("\nauthorization {\n include \"auth.json\" \n}\n")
	err = ioutil.WriteFile("./data/current/main.conf", []byte(config), 0666)
	if err != nil {
		t.Fatal(err)
	}

	natsd, _ := gnatsd.RunServerWithConfig("./data/current/main.conf")
	if natsd == nil {
		t.Fatal("Unexpected error starting a configured NATS server")
	}
	defer natsd.Shutdown()

	errCh := make(chan error, 2)
	ncA, err := nats.Connect("nats://user-a:secret@127.0.0.1:4222",
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			errCh <- err
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ncA.Close()
	ncA.Publish("ng.1", []byte("first"))
	ncA.Flush()

	ncB, err := nats.Connect("nats://user-b:secret@127.0.0.1:4222",
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			errCh <- err
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ncB.Close()
	ncB.Publish("ng.2", []byte("second"))
	ncB.Flush()

	select {
	case err := <-errCh:
		got := err.Error()
		expected := `nats: Permissions Violation for Publish to "ng.1"`
		if got != expected {
			t.Errorf("Expected %q, got: %q", expected, got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timed out waiting for server to stop")
	}

	select {
	case err := <-errCh:
		got := err.Error()
		expected := `nats: Permissions Violation for Publish to "ng.2"`
		if got != expected {
			t.Errorf("Expected %q, got: %q", expected, got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timed out waiting for server to stop")
	}
}

func TestFullCycleWithAccounts(t *testing.T) {
	// Create a data directory.
	opts := DefaultOptions()
	opts.DataDir = "./data-accounts"
	s := server.NewServer(opts)
	host := fmt.Sprintf("http://%s:%d", opts.Host, opts.Port)
	ctx, _ := context.WithTimeout(context.Background(), 5*time.Second)
	time.AfterFunc(2*time.Second, func() {
		s.Shutdown(ctx)
		waitServerIsDone(t, ctx, host)
	})
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		done <- struct{}{}
	}()
	waitServerIsReady(t, ctx, host)

	// Create a couple of users
	payload := `{
	  "username": "foo-user",
	  "password": "secret",
          "permissions": "normal-user",
          "account": "foo"
	}`
	resp, _, err := curl("PUT", host+"/v1/auth/idents/foo-user", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("Expected OK, got: %v", resp.StatusCode)
	}

	payload = `{
	  "username": "bar-user",
	  "password": "secret",
          "permissions": "normal-user",
          "account": "bar"
	}`
	resp, _, err = curl("PUT", host+"/v1/auth/idents/bar-user", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("Expected OK, got: %v", resp.StatusCode)
	}

	payload = `{
	  "username": "quux-user",
	  "password": "secret",
          "permissions": "normal-user"
	}`
	resp, _, err = curl("PUT", host+"/v1/auth/idents/quux-user", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("Expected OK, got: %v", resp.StatusCode)
	}

	// Create the permissions.
	payload = `{
         "publish": {
           "allow": ["foo", "bar"]
          },
          "subscribe": {
            "deny": ["quux"]
          }
	}`
	resp, _, err = curl("PUT", host+"/v1/auth/perms/normal-user", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("Expected OK, got: %v", resp.StatusCode)
	}

	// Create a snapshot.
	resp, _, err = curl("POST", host+"/v1/auth/snapshot?name=with-accounts", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("Expected OK, got: %v", resp.StatusCode)
	}

	// Publish a named snapshot.
	resp, _, err = curl("POST", host+"/v1/auth/publish?name=with-accounts", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("Expected OK, got: %v", resp.StatusCode)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for server to stop")
	}

	config := `
          # Load the generated accounts.
          include "auth.json"

          authorization {
            # Add users to the global account.
            users = $users
          }

          # Create the users bound to different accounts.
          accounts = $accounts
        `

	err = ioutil.WriteFile("./data-accounts/current/main.conf", []byte(config), 0666)
	if err != nil {
		t.Fatal(err)
	}

	natsd, _ := gnatsd.RunServerWithConfig("./data-accounts/current/main.conf")
	if natsd == nil {
		t.Fatal("Unexpected error starting a configured NATS server")
	}
	defer natsd.Shutdown()

	errCh := make(chan error, 2)
	ncA, err := nats.Connect("nats://foo-user:secret@127.0.0.1:4222",
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			errCh <- err
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ncA.Close()
	ncA.Publish("ng.1", []byte("first"))
	ncA.Flush()

	ncB, err := nats.Connect("nats://bar-user:secret@127.0.0.1:4222",
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			errCh <- err
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ncB.Close()
	ncB.Publish("ng.2", []byte("second"))
	ncB.Flush()

	ncC, err := nats.Connect("nats://quux-user:secret@127.0.0.1:4222",
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			errCh <- err
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ncC.Close()
	ncC.Publish("ng.3", []byte("third"))
	ncC.Flush()

	select {
	case err := <-errCh:
		got := err.Error()
		expected := `nats: Permissions Violation for Publish to "ng.1"`
		if got != expected {
			t.Errorf("Expected %q, got: %q", expected, got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timed out waiting for server to stop")
	}

	select {
	case err := <-errCh:
		got := err.Error()
		expected := `nats: Permissions Violation for Publish to "ng.2"`
		if got != expected {
			t.Errorf("Expected %q, got: %q", expected, got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timed out waiting for server to stop")
	}

	// Users from different accounts should not be able to
	// receive messages between them.
	sA, err := ncA.SubscribeSync("foo")
	if err != nil {
		t.Fatal(err)
	}
	ncA.Flush()
	sB, err := ncB.SubscribeSync("foo")
	if err != nil {
		t.Fatal(err)
	}
	ncB.Flush()
	sC, err := ncC.SubscribeSync("foo")
	if err != nil {
		t.Fatal(err)
	}
	ncC.Flush()

	err = ncB.Publish("foo", []byte("hello world"))
	if err != nil {
		t.Fatal(err)
	}
	ncB.Flush()

	err = ncC.Publish("hello", []byte("world"))
	if err != nil {
		t.Fatal(err)
	}
	ncC.Flush()

	// Connections A and C will not receive the message.
	_, err = sA.NextMsg(500 * time.Millisecond)
	if err == nil {
		t.Error("Expected timeout waiting for message")
	}
	_, err = sC.NextMsg(500 * time.Millisecond)
	if err == nil {
		t.Error("Expected timeout waiting for message")
	}

	// Connection B is sending the message so will receive it.
	_, err = sB.NextMsg(1 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
}
