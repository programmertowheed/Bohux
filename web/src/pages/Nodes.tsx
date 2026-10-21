import { useQuery } from "@connectrpc/connect-query";
import { NodeService, NodeState } from "../gen/bohux/v1/node_pb";
import { Failed, Loading, Table } from "../components/Table";
import { bytes, enumName, when } from "../format";

export function Nodes() {
  const q = useQuery(NodeService.method.listNodes, { pageSize: 100 }, { refetchInterval: 10000 });
  if (q.isPending) return <Loading what="nodes" />;
  if (q.error) return <Failed error={q.error} />;
  return (
    <section>
      <h1 className="mb-4 text-xl font-semibold">Nodes</h1>
      <Table
        head={["Node", "State", "Agent", "Last seen", "CPU", "Memory"]}
        empty="No nodes yet. Register one with: bohux node token <slug>"
        rows={q.data.nodes.map((n) => [
          n.slug,
          <span className={n.state === NodeState.ONLINE ? "text-ok" : "text-danger"}>
            {enumName("NODE_STATE_", NodeState[n.state])}
          </span>,
          n.agentVersion,
          when(n.lastSeenAt),
          n.usage ? `${Math.round(n.usage.cpuBusy * 100)}% of ${n.usage.cpuCores}` : "-",
          n.usage
            ? `${bytes(n.usage.memoryTotalBytes - n.usage.memoryAvailableBytes)} / ${bytes(n.usage.memoryTotalBytes)}`
            : "-",
        ])}
      />
    </section>
  );
}
