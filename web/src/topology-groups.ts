import type { Row } from "./topology";

// Connected components of observed server addresses. A DNS name alone is not
// evidence of shared capacity. Multi-address DNS sets may overlap transitively.
export function ipGroups(rows: Row[]) {
  const parent = rows.map((_, i) => i);
  const find = (i: number): number =>
    parent[i] === i ? i : (parent[i] = find(parent[i]));
  const owners = new Map<string, number>();
  rows.forEach((row, i) => {
    for (const address of row.server_ips) {
      const ip = address.trim().toLowerCase();
      if (!ip) continue;
      const owner = owners.get(ip);
      if (owner !== undefined) parent[find(i)] = find(owner);
      else owners.set(ip, i);
    }
  });
  const groups = new Map<number, Row[]>();
  rows.forEach((row, i) => {
    const key = find(i);
    groups.set(key, [...(groups.get(key) || []), row]);
  });
  return [...groups.values()]
    .map((members) => {
      members.sort((a, b) =>
        (a.worker + a.engine).localeCompare(b.worker + b.engine),
      );
      const ips = [
        ...new Set(
          members.flatMap((r) =>
            r.server_ips.map((ip) => ip.trim().toLowerCase()).filter(Boolean),
          ),
        ),
      ].sort();
      // Unknown addresses remain independent even when upstream group is empty.
      const key = ips.length
        ? ips.join("|")
        : "unknown:" + members[0].worker + ":" + members[0].engine;
      return { id: "ip-group:" + key, ips, members };
    })
    .sort((a, b) => a.id.localeCompare(b.id));
}
