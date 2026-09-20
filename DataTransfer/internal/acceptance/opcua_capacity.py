"""Actual OPC-UA subscription peer, with per-monitored-item notification queues."""
import argparse
import asyncio
import json
import logging
import sys
from datetime import datetime, timezone

from asyncua import Server, ua


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, required=True)
    args = parser.parse_args()
    server = Server()
    await server.init()
    server.set_endpoint(f"opc.tcp://127.0.0.1:{args.port}")
    server.set_security_policy([ua.SecurityPolicyType.NoSecurity])
    namespace = await server.register_namespace("urn:smartfactory:capacity")
    nodes = [await server.nodes.objects.add_variable(
        ua.NodeId(f"sample-{i}", namespace), f"sample-{i}",
        ua.Variant(0, ua.VariantType.UInt32)) for i in range(100)]
    async with server:
        print(json.dumps({"namespace": namespace, "implementation": "asyncua"}), flush=True)
        while True:
            line = await asyncio.to_thread(sys.stdin.readline)
            if not line:
                break
            request = json.loads(line)
            sequence = request["sequence"]
            stamp = datetime.now(timezone.utc)
            for node in nodes:
                value = ua.DataValue(ua.Variant(sequence, ua.VariantType.UInt32))
                value.SourceTimestamp = stamp
                value.ServerTimestamp = stamp
                await node.write_value(value)
            print(json.dumps({"sequence": sequence, "count": len(nodes)}), flush=True)


logging.basicConfig(level=logging.ERROR)
asyncio.run(main())
