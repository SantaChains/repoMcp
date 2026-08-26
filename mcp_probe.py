"""用真实 MCP Python SDK 连接本服务，复刻 MCP 消费方的 Streamable HTTP 客户端代码路径。

消费方侧对应实现见其 provider/tools/loaders/mcp.py 的 _streamable_http_session：
注入自建 httpx.AsyncClient（带 headers/timeout/follow_redirects），
进入 streamable_http_client，再在同一上下文内 ClientSession.initialize()。

可用环境变量覆盖，换部署无需改脚本：
  REPOMCP_PROBE_URL    服务地址，默认 http://127.0.0.1:8790/mcp
  REPOMCP_PROBE_TOKEN  Bearer 令牌，默认 test-token-abc123
  REPOMCP_PROBE_REPO   仓库短名；不设置时从 repo_overview 自动发现
  REPOMCP_PROBE_SYMBOL 要查的符号名；不设置则跳过 find_symbol 检查
  REPOMCP_PROBE_QUERY  检索关键词；默认 "main"
"""

import os

import anyio
import httpx
from mcp import ClientSession

URL = os.getenv("REPOMCP_PROBE_URL", "http://127.0.0.1:8790/mcp")
HEADERS = {"Authorization": "Bearer " + os.getenv("REPOMCP_PROBE_TOKEN", "test-token-abc123")}

PROBE_REPO = os.getenv("REPOMCP_PROBE_REPO", "").strip()
PROBE_SYMBOL = os.getenv("REPOMCP_PROBE_SYMBOL", "").strip()
PROBE_QUERY = os.getenv("REPOMCP_PROBE_QUERY", "").strip() or "main"


async def resolve_repo(session):
    """显式配置优先；否则从 repo_overview 首行「## <短名> @sha (ref)」解析。"""
    if PROBE_REPO:
        return PROBE_REPO
    try:
        r = await session.call_tool("repo_overview", {})
        text = r.model_dump(by_alias=True)["content"][0]["text"]
        for line in text.splitlines():
            line = line.strip()
            if line.startswith("## "):
                name = line[3:].split(" ", 1)[0].strip()
                if name:
                    return name
    except Exception:
        pass
    return ""


async def open_transport(stack):
    """兼容新旧两种 SDK 入口。"""
    try:
        from mcp.client.streamable_http import streamable_http_client

        client = await stack.enter_async_context(
            httpx.AsyncClient(headers=HEADERS, timeout=15, follow_redirects=True)
        )
        return await stack.enter_async_context(
            streamable_http_client(URL, http_client=client)
        ), "streamable_http_client(http_client=...)"
    except ImportError:
        from mcp.client.streamable_http import streamablehttp_client

        return await stack.enter_async_context(
            streamablehttp_client(URL, headers=HEADERS, timeout=15)
        ), "streamablehttp_client(headers=...)"


async def main():
    from contextlib import AsyncExitStack

    async with AsyncExitStack() as stack:
        transport, entry = await open_transport(stack)
        read, write = transport[0], transport[1]
        print(f"transport   : {entry}")

        session = await stack.enter_async_context(ClientSession(read, write))
        init = await session.initialize()
        d = init.model_dump(by_alias=True)
        print(f"protocol    : {d['protocolVersion']}")
        print(f"serverInfo  : {d['serverInfo']['name']} {d['serverInfo']['version']}")
        print(f"capabilities: {d['capabilities']}")
        print(f"instructions: {len(d.get('instructions') or '')} 字符")

        tools = (await session.list_tools()).tools
        print(f"\ntools ({len(tools)}):")
        for t in tools:
            td = t.model_dump(by_alias=True)
            print(f"  - {td['name']:<14} required={td['inputSchema'].get('required', [])}")

        repo = await resolve_repo(session)
        print(f"\n目标仓库   : {repo or '（未能解析，跳过仓库相关检查）'}")

        def show(r, n=6):
            rd = r.model_dump(by_alias=True)
            print(f"isError={rd['isError']}")
            print("\n".join(rd["content"][0]["text"].splitlines()[:n]))

        if not repo:
            print("\n跳过仓库相关 tools/call（无法确定仓库短名，可设 REPOMCP_PROBE_REPO）")
        else:
            print("\n--- tools/call: find_symbol ---")
            if PROBE_SYMBOL:
                show(await session.call_tool("find_symbol", {"name": PROBE_SYMBOL, "repo": repo}))
            else:
                print("（REPOMCP_PROBE_SYMBOL 未设置，跳过）")

            print(f"\n--- tools/call: search_code（query={PROBE_QUERY!r}）---")
            show(await session.call_tool("search_code", {"query": PROBE_QUERY, "repo": repo, "k": 2}), 5)

            print("\n--- tools/call: 错误路径应为 isError 而非协议异常 ---")
            show(await session.call_tool("read_file", {"repo": repo, "path": "zzqqxx_not_exist.go"}), 2)

        print("\n--- ping ---")
        await session.send_ping()
        print("ping ok")

    print("\nALL OK")


anyio.run(main)
