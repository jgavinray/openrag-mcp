# Bender: Herald — Migrate to Streamable HTTP Transport

**Repo:** `/Users/jgavinray/dev/openrag-mcp/`  
**SSH:** `zoidberg@192.168.0.44` (hyper01)
**Kanban:** No ticket yet — file one as OPS- or create HERALD-1 if board supports it. Or just report status and I'll file it.

---

## Problem

Herald (`openrag-mcp`) currently uses `ServeSSE` which calls `mcpserver.NewSSEServer` — the **deprecated** HTTP+SSE transport from MCP spec 2024-11-05. The correct transport is **Streamable HTTP** per spec 2025-06-18.

`mcp-go` (the Go MCP library Herald uses) already supports Streamable HTTP — it just isn't being used.

---

## What to Do

### 1. Check mcp-go Streamable HTTP API
```bash
cd /Users/jgavinray/dev/openrag-mcp
grep -r "Streamable\|StreamableHTTP\|NewStreamable" $(go env GOPATH)/pkg/mod/github.com/mark3labs/ 2>/dev/null | head -20
# Or:
grep -r "NewStreamable\|StreamableHTTP" vendor/ 2>/dev/null | head -20
# Or check the mcp-go source directly:
ls $(go env GOPATH)/pkg/mod/github.com/mark3labs/mcp-go@*/server/
```

### 2. Update `internal/mcp/server.go`
Replace `ServeSSE` with a `ServeStreamableHTTP` method that uses `mcp-go`'s Streamable HTTP server. The API likely looks like:
```go
// Instead of:
sseSrv := mcpserver.NewSSEServer(mcpSrv, mcpserver.WithBaseURL(baseURL), ...)

// Use:
streamableSrv := mcpserver.NewStreamableHTTPServer(mcpSrv, ...)
```

Check the actual mcp-go API and use whatever method it provides for Streamable HTTP. Do not guess — read the source.

### 3. Update `cmd/herald/main.go`
The `http` transport case currently calls `srv.ServeSSE(...)`. Update it to call the new `ServeStreamableHTTP` method. Keep the same env var interface (`HERALD_TRANSPORT=http`).

### 4. Update `go.mod` if needed
If the Streamable HTTP server requires a newer version of `mcp-go`, bump it:
```bash
go get github.com/mark3labs/mcp-go@latest
go mod tidy
```

### 5. Build and test
```bash
cd /Users/jgavinray/dev/openrag-mcp
go build ./...
go test ./...

# Smoke test HTTP mode:
OPENRAG_URL=http://192.168.0.44:3000 OPENRAG_API_KEY=orag_eqFlA1iI5ySiWrT34y1JTz0GMfsPQL-s3sIGIX6gEOo HERALD_TRANSPORT=http HERALD_PORT=9999 ./herald &
sleep 1
curl -s -X POST http://127.0.0.1:9999/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-06-18' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0.1"}}}'
kill %1
```

### 6. Commit
```bash
cd /Users/jgavinray/dev/openrag-mcp
git add -A
git commit -m "feat: migrate HTTP transport to Streamable HTTP (MCP spec 2025-06-18)

Problem: Herald was using the deprecated HTTP+SSE transport (spec 2024-11-05)
via mcpserver.NewSSEServer. This is superseded by Streamable HTTP.

Solution: Updated ServeSSE -> ServeStreamableHTTP using mcp-go's Streamable
HTTP server implementation. Single /mcp endpoint supports both POST and GET.
HERALD_TRANSPORT=http interface unchanged.

Notes: mcp-go already supports Streamable HTTP. No library migration needed."
git push
```

### 7. Rebuild and redeploy herald on hyper01 if it's running there
```bash
# Check if herald is deployed on hyper01
ssh zoidberg@192.168.0.44 'docker ps | grep herald 2>/dev/null || echo "not running"'
# If running, rebuild and redeploy
```

---

## Definition of Done
- [ ] `ServeSSE` replaced with Streamable HTTP equivalent using mcp-go
- [ ] `POST http://host:port/mcp` returns valid MCP response
- [ ] Tests pass
- [ ] Committed and pushed
- [ ] Hyper01 redeployed if applicable

## Checkpoint
Append status to `/Users/jgavinray/dev/openrag-mcp/bender-herald-checkpoint.md`
