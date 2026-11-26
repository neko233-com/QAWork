// Package mcp exposes QAWork to agents over the Model Context Protocol
// (stdio, newline-delimited JSON-RPC 2.0): list devices, run a capture,
// gate a CSV, and inspect the environment. Agents get the same samplers
// and CSV as the GUI, not a second implementation.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/neko233-com/QAWork/internal/version"
)

// Tool is one callable exposed by the server.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
	Run         func(ctx context.Context, args map[string]any) (any, error)
}

// Server answers MCP requests. Tools is injectable so tests do not touch adb.
type Server struct {
	Version      string
	Instructions string
	Tools        []Tool
}

// NewServer returns the QAWork MCP server with the default tools.
func NewServer() *Server {
	return &Server{
		Version: version.Version,
		Instructions: "QAWork 是游戏 QA 工作台。Android 是首选验证平台：先 qawork_devices 找设备，" +
			"再 qawork_perf 采一段（serial 可填 local 采本机进程，但 PC 没有帧率来源），" +
			"最后用 qawork_report 复判 CSV。所有数字来自 adb / SurfaceFlinger / gfxinfo / proc，不造假。",
		Tools: defaultTools(),
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Serve reads requests until EOF or ctx is done, writing one response per
// request line. Notifications produce no response.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		resp, notify, err := s.Handle(ctx, line)
		if err != nil {
			return err
		}
		if notify {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

// Handle parses and answers one JSON-RPC message.
func (s *Server) Handle(ctx context.Context, line []byte) (rpcResponse, bool, error) {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "解析失败: " + err.Error()}}, false, nil
	}
	notify := len(req.ID) == 0
	if req.Method == "" {
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32600, Message: "缺少 method"}}, notify, nil
	}
	switch req.Method {
	case "initialize":
		return s.ok(req.ID, map[string]any{
			"protocolVersion": protocolVersion(req.Params),
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "qawork", "version": s.Version},
			"instructions":    s.Instructions,
		}), notify, nil
	case "notifications/initialized", "initialized":
		return rpcResponse{}, true, nil
	case "ping":
		return s.ok(req.ID, map[string]any{}), notify, nil
	case "tools/list":
		tools := make([]map[string]any, 0, len(s.Tools))
		for _, t := range s.Tools {
			tools = append(tools, map[string]any{
				"name": t.Name, "description": t.Description, "inputSchema": t.Schema,
			})
		}
		return s.ok(req.ID, map[string]any{"tools": tools}), notify, nil
	case "tools/call":
		return s.call(ctx, req), notify, nil
	default:
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "未知方法 " + req.Method}}, notify, nil
	}
}

func (s *Server) ok(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func (s *Server) call(ctx context.Context, req rpcRequest) rpcResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "tools/call 参数无效: " + err.Error()}}
	}
	var tool *Tool
	for i := range s.Tools {
		if s.Tools[i].Name == params.Name {
			tool = &s.Tools[i]
			break
		}
	}
	if tool == nil {
		return s.ok(req.ID, toolError(fmt.Sprintf("未知工具 %q", params.Name)))
	}
	args := map[string]any{}
	if len(params.Arguments) > 0 && string(params.Arguments) != "null" {
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return s.ok(req.ID, toolError("arguments 必须是对象: "+err.Error()))
		}
	}
	result, err := tool.Run(ctx, args)
	if err != nil {
		return s.ok(req.ID, toolError(err.Error()))
	}
	text, _ := json.MarshalIndent(result, "", "  ")
	return s.ok(req.ID, map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(text)}},
		"structuredContent": result,
		"isError":           false,
	})
}

func toolError(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	}
}

func protocolVersion(params json.RawMessage) string {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &p)
	}
	if strings.TrimSpace(p.ProtocolVersion) != "" {
		return p.ProtocolVersion
	}
	return "2025-06-18"
}
