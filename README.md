# Herald

Herald is a [Model Context Protocol (MCP)](https://modelcontextprotocol.io) server that bridges AI coding agents to an [OpenRAG](https://github.com/jgavinray/openrag) deployment. It exposes your OpenRAG knowledge base as MCP tools so agents like OpenCode, Claude Code, or any MCP-aware client can search and retrieve context from your own infrastructure. Written in Go, licensed under GPL v2.

---

## Prerequisites

- **Go 1.21+** — for building from source
- **Docker** *(optional)* — for containerised deployment
- A running **OpenRAG** instance (URL + API key)

---

## Build from Source

```bash
git clone https://github.com/jgavinray/openrag-mcp
cd openrag-mcp
go build -o herald ./cmd/herald
```

---

## Run the Binary

```bash
export OPENRAG_URL=http://your-openrag:3000
export OPENRAG_API_KEY=your_key
./herald
```

Herald starts an MCP server on stdin/stdout, ready for any MCP client to connect.

---

## Run with Docker

```bash
docker build -t herald .
docker run --env-file .env herald
```

---

## Run with docker-compose

```bash
cp .env.example .env
# edit .env with your values
docker compose up
```

---

## OpenCode Integration

> **This is the primary use case.** Wire Herald into OpenCode so your coding agent has live access to your OpenRAG knowledge base during every session.

Add the following to `~/.config/opencode/opencode.json` for a global setup, or to a project-local `opencode.json` to scope it to one repo:

```json
{
  "mcp": {
    "openrag": {
      "type": "local",
      "command": "/path/to/herald",
      "environment": {
        "OPENRAG_URL": "http://your-openrag:3000",
        "OPENRAG_API_KEY": "your_key"
      }
    }
  }
}
```

Replace `/path/to/herald` with the absolute path to the built binary (e.g. `~/bin/herald` or wherever you installed it).

Once configured, OpenCode will launch Herald automatically and make its tools available to the agent on every run.

---

## Environment Variables

| Variable         | Required | Description                              |
|------------------|----------|------------------------------------------|
| `OPENRAG_URL`    | Yes      | Base URL of your OpenRAG deployment      |
| `OPENRAG_API_KEY`| Yes      | API key for OpenRAG authentication       |

---

## License

This project is licensed under the GNU General Public License v2.0. See [LICENSE](LICENSE) for details.
