package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

type RPCNotification struct {
	Method string
	Params json.RawMessage
	Raw    json.RawMessage
}

type AppServerRPC interface {
	Call(context.Context, string, any, any) error
	Notify(context.Context, string, any) error
	Notifications() <-chan RPCNotification
	Close() error
}

type rpcResponse struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type AppServerClient struct {
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	writeMu       sync.Mutex
	stateMu       sync.Mutex
	nextID        int64
	pending       map[int64]chan rpcResponse
	notifications chan RPCNotification
	done          chan struct{}
	readErr       error
}

func StartAppServer(ctx context.Context, binary string) (*AppServerClient, error) {
	if binary == "" {
		binary = "codex"
	}
	cmd := exec.CommandContext(ctx, binary, "app-server", "--listen", "stdio://")
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Codex app-server: %w", err)
	}
	client := &AppServerClient{cmd: cmd, stdin: stdin, pending: make(map[int64]chan rpcResponse), notifications: make(chan RPCNotification, 64), done: make(chan struct{})}
	go client.readLoop(stdout)
	initialize := map[string]any{"clientInfo": map[string]string{"name": "goalforge", "title": "GoalForge", "version": "0.1.0"}, "capabilities": map[string]any{"experimentalApi": true}}
	var response map[string]any
	if err = client.Call(ctx, "initialize", initialize, &response); err != nil {
		_ = client.Close()
		return nil, err
	}
	if err = client.Notify(ctx, "initialized", map[string]any{}); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func (c *AppServerClient) Call(ctx context.Context, method string, params any, result any) error {
	c.stateMu.Lock()
	c.nextID++
	id := c.nextID
	response := make(chan rpcResponse, 1)
	c.pending[id] = response
	c.stateMu.Unlock()
	if err := c.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		c.stateMu.Lock()
		delete(c.pending, id)
		c.stateMu.Unlock()
		return err
	}
	select {
	case <-ctx.Done():
		c.stateMu.Lock()
		delete(c.pending, id)
		c.stateMu.Unlock()
		return ctx.Err()
	case <-c.done:
		return c.connectionError()
	case message := <-response:
		if message.Error != nil {
			return fmt.Errorf("Codex app-server %s (%d): %s", method, message.Error.Code, message.Error.Message)
		}
		if result == nil || len(message.Result) == 0 {
			return nil
		}
		if err := json.Unmarshal(message.Result, result); err != nil {
			return fmt.Errorf("decode Codex app-server %s response: %w", method, err)
		}
		return nil
	}
}

func (c *AppServerClient) Notify(_ context.Context, method string, params any) error {
	return c.write(map[string]any{"method": method, "params": params})
}

func (c *AppServerClient) Notifications() <-chan RPCNotification { return c.notifications }

func (c *AppServerClient) write(message any) error {
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(append(raw, '\n'))
	return err
}

func (c *AppServerClient) readLoop(reader io.Reader) {
	defer close(c.done)
	defer close(c.notifications)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		raw := append([]byte(nil), scanner.Bytes()...)
		var envelope struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			continue
		}
		if envelope.ID != nil && envelope.Method == "" {
			var response rpcResponse
			if json.Unmarshal(raw, &response) == nil {
				c.stateMu.Lock()
				waiter := c.pending[response.ID]
				delete(c.pending, response.ID)
				c.stateMu.Unlock()
				if waiter != nil {
					waiter <- response
				}
			}
			continue
		}
		if envelope.ID == nil && envelope.Method != "" {
			c.notifications <- RPCNotification{Method: envelope.Method, Params: envelope.Params, Raw: raw}
		}
	}
	c.stateMu.Lock()
	c.readErr = scanner.Err()
	c.stateMu.Unlock()
}

func (c *AppServerClient) connectionError() error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.readErr != nil {
		return c.readErr
	}
	return errors.New("Codex app-server connection closed")
}

func (c *AppServerClient) Close() error {
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	return c.cmd.Wait()
}
