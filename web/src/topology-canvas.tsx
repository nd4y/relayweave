import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  Paper,
  Stack,
  Tooltip,
  Typography,
  useMediaQuery,
  useTheme,
} from "@mui/material";
import {
  Background,
  Controls,
  Handle,
  MiniMap,
  Position,
  ReactFlow,
  applyNodeChanges,
  type Node as FlowNode,
  type NodeProps,
  type ReactFlowInstance,
} from "@xyflow/react";
import { RouteSparklines } from "./topology-sparklines";
import type { Node, Edge } from "./topology";
import "@xyflow/react/dist/style.css";
import "./topology.css";

type CardData = Record<string, unknown> & {
  model: Node;
  active: number;
  direction: string;
  mobile: boolean;
  choose: (n: Node) => void;
  toggle: (id: string) => void;
};
type CardNode = FlowNode<CardData, "route">;
const RouteCard = memo(function RouteCard({
  data,
  selected,
}: NodeProps<CardNode>) {
  const { model: n } = data;
  const group = n.type === "группа серверных IP";
  const name =
    n.type === "профиль" && n.row
      ? `${n.row.egress ? n.row.egress + " · " : ""}${n.row.server_host || "Профиль"} · ${(n.row.profile_id || n.row.id).slice(0, 8)}`
      : n.label;
  const invalid =
    n.members?.filter((r) => r.state === "core_not_ready").length || 0;
  return (
    <Paper
      variant="outlined"
      className="rw-flow-card"
      data-testid="topology-card"
      data-model-id={n.id}
      sx={{
        height: "100%",
        width: "100%",
        overflow: "hidden",
        borderColor: selected
          ? "primary.main"
          : invalid
            ? "warning.main"
            : "divider",
        borderWidth: selected ? 2 : 1,
      }}
    >
      <Handle type="target" position={Position.Left} isConnectable={false} />
      <Box
        sx={{
          p: 1,
          display: "grid",
          gridTemplateColumns: data.mobile ? "1fr" : "minmax(0, 1fr) 238px",
          gap: data.mobile ? 0.5 : 1.5,
          alignItems: "start",
          "& .rw-card-heading": { minWidth: 0 },
          "& .rw-card-title": { lineHeight: 1.25 },
          "& .MuiTypography-body2": { fontSize: 14, lineHeight: 1.5 },
        }}
      >
        <Box className="rw-card-heading">
          <Typography variant="body2" color="text.secondary">
            {n.type}
            {n.members?.some(r => r.egress) ? " · " + [...new Set(n.members.map(r => r.egress).filter(Boolean))].join(" / ") : ""}
            {n.type === "профиль" && n.row ? ` · ${n.row.version}` : ""}
          </Typography>
          <Tooltip title={n.label} enterDelay={350}>
            <Typography
              variant="body1"
              className="rw-card-title"
              fontWeight={600}
              sx={{
                overflowWrap: "anywhere",
                display: "-webkit-box",
                WebkitLineClamp: 2,
                WebkitBoxOrient: "vertical",
                overflow: "hidden",
              }}
            >
              {name}
            </Typography>
          </Tooltip>
          {n.members?.length ? (
            <>
              <Typography variant="body2" display="block">
                {data.active} соединений
                {invalid ? ` · не готовы: ${invalid}` : ""}
              </Typography>
            </>
          ) : (
            <Typography variant="body2" color="text.secondary">
              Настроенная связь
            </Typography>
          )}
          {group && (
            <Button
              size="small"
              className="nodrag nopan"
              aria-expanded={!!n.expanded}
              onClick={(e) => {
                e.stopPropagation();
                data.toggle(n.id);
              }}
              sx={{ mt: 0.5 }}
            >
              {n.expanded
                ? "Свернуть"
                : `Раскрыть профили (${n.members?.length || 0})`}
            </Button>
          )}
        </Box>
        <RouteSparklines
          members={n.members}
          direction={data.direction}
          mobile={data.mobile}
        />
      </Box>
      <Handle type="source" position={Position.Right} isConnectable={false} />
    </Paper>
  );
});
const nodeTypes = { route: RouteCard };

export function TopologyCanvas({
  graph,
  selected,
  related,
  pinned,
  choose,
  toggleGroup,
  direction,
  resetKey,
  fullscreen,
}: {
  graph: { nodes: Node[]; edges: Edge[] };
  selected: Node | null;
  related: Set<string>;
  pinned: Set<string>;
  choose: (n: Node) => void;
  toggleGroup: (id: string) => void;
  direction: string;
  resetKey: number;
  fullscreen: boolean;
}) {
  const theme = useTheme();
  const reduced = useMediaQuery("(prefers-reduced-motion: reduce)");
  const mobile = useMediaQuery("(max-width: 800px)");
  const [nodes, setNodes] = useState<CardNode[]>([]),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [focus, setFocus] = useState(false),
    [retry, setRetry] = useState(0);
  const instance = useRef<ReactFlowInstance<CardNode> | null>(null);
  const engine = useRef<ReturnType<
    (typeof import("./topology-layout"))["createLayoutEngine"]
  > | null>(null);
  const dimensions = useMemo(
    () =>
      new Map(
        graph.nodes.map((n) => [
          n.id,
          {
            width: mobile ? 340 : 480,
            height: n.expanded
              ? (mobile ? 264 : 188) +
                (n.members?.length || 0) * (mobile ? 264 : 176)
              : mobile
                ? 252
                : 164,
          },
        ]),
      ),
    [graph, mobile],
  );
  const structure = JSON.stringify({
    nodes: graph.nodes
      .map((n) => [n.id, n.parentId, dimensions.get(n.id)])
      .sort((a, b) => String(a[0]).localeCompare(String(b[0]))),
    edges: graph.edges.map((e) => [e.from, e.to]).sort(),
  });
  const latest = useRef(graph);
  latest.current = graph;
  const selection = useRef({ selected, choose });
  selection.current = { selected, choose };
  const handleSelection = useCallback(
    ({ nodes: chosen }: { nodes: CardNode[] }) => {
      if (
        chosen.length === 1 &&
        chosen[0].id !== selection.current.selected?.id
      )
        selection.current.choose(chosen[0].data.model);
    },
    [],
  );
  const positions = useRef(new Map<string, { x: number; y: number }>());
  const latestPinned = useRef(pinned);
  latestPinned.current = pinned;
  const firstFit = useRef(true),
    layoutCount = useRef(0);
  useEffect(
    () => () => {
      engine.current?.terminateWorker();
    },
    [],
  );

  function readableOverview() {
    const v = instance.current;
    const host = document.querySelector('[data-testid="topology-flow"]');
    if (!v || !host) return;
    const roots = latest.current.nodes.filter((n) => !n.parentId);
    const right = Math.max(
      0,
      ...roots.map(
        (n) =>
          (positions.current.get(n.id)?.x || 0) +
          (dimensions.get(n.id)?.width || 300),
      ),
    );
    const top = Math.min(
      0,
      ...roots.map((n) => positions.current.get(n.id)?.y || 0),
    );
    // Keep the IP column readable and complete, rather than centering oversized
    // bounds and clipping the labels at the top and right edges. Pan to sources.
    void v.setViewport({
      x: Math.min(24, host.clientWidth - right - 24),
      y: 12 - top,
      zoom: 1,
    });
  }

  // No telemetry fields in dependencies: counter updates never re-run ELK.
  useEffect(() => {
    let cancelled = false;
    const started = performance.now();
    const model = latest.current;
    const roots = model.nodes.filter((n) => !n.parentId);
    if (!roots.length) {
      positions.current.clear();
      setNodes([]);
      return;
    }
    setBusy(true);
    setError("");
    let timer: ReturnType<typeof setTimeout> | undefined;
    let bootTimer: ReturnType<typeof setTimeout> | undefined;
    const grid = () => {
      const laneY = new Map<number, number>();
      return new Map<string, { x: number; y: number }>(
        model.nodes.map((n) => {
          if (n.parentId)
            return [
              n.id,
              {
                x: 12,
                y:
                  (mobile ? 264 : 176) +
                  (n.childIndex || 0) * (mobile ? 264 : 176),
              },
            ];
          const y = laneY.get(n.lane) || 0;
          laneY.set(n.lane, y + (dimensions.get(n.id)?.height || 164) + 12);
          return [n.id, { x: n.lane * (mobile ? 388 : 528), y }];
        }),
      );
    };
    if (!positions.current.size) {
      positions.current = grid();
      refresh();
      requestAnimationFrame(readableOverview);
    }
    void (async () => {
      const { createLayoutEngine } = await import("./topology-layout");
      if (cancelled) return;
      if (!engine.current) engine.current = createLayoutEngine();
      // Download/parse/worker boot is separate from the computation deadline.
      // A usable grid is already visible while a cold browser loads the code.
      await Promise.race([
        engine.current.knownLayoutAlgorithms(),
        new Promise<never>((_, reject) => {
          bootTimer = setTimeout(
            () => reject(new Error("Загрузка ELK заняла больше 30 секунд")),
            30000,
          );
        }),
      ]);
      clearTimeout(bootTimer);
      if (cancelled) return;
      const result = await Promise.race([
        engine.current.layout({
          id: "root",
          layoutOptions: {
            "elk.algorithm": "layered",
            "elk.direction": "RIGHT",
            "elk.edgeRouting": "ORTHOGONAL",
            "elk.spacing.nodeNode": "12",
            "elk.padding": "[top=8,left=8,bottom=8,right=8]",
            "elk.layered.spacing.nodeNodeBetweenLayers": "48",
          },
          children: roots.map((n) => ({ id: n.id, ...dimensions.get(n.id) })),
          edges: model.edges
            .filter(
              (e) =>
                roots.some((n) => n.id === e.from) &&
                roots.some((n) => n.id === e.to),
            )
            .map((e, i) => ({
              id: "edge-" + i,
              sources: [e.from],
              targets: [e.to],
            })),
        }),
        new Promise<never>((_, reject) => {
          timer = setTimeout(
            () => reject(new Error("Раскладка заняла больше 8 секунд")),
            8000,
          );
        }),
      ]);
      if (cancelled) return;
      const next = new Map<string, { x: number; y: number }>();
      result.children?.forEach((n) =>
        next.set(n.id, { x: n.x || 0, y: n.y || 0 }),
      );
      model.nodes
        .filter((n) => n.parentId)
        .forEach((n) =>
          next.set(n.id, {
            x: 12,
            y:
              (mobile ? 264 : 176) + (n.childIndex || 0) * (mobile ? 264 : 176),
          }),
        );
      for (const id of latestPinned.current) {
        const old = positions.current.get(id);
        if (old && next.has(id)) next.set(id, old);
      }
      positions.current = next;
      layoutCount.current++;
      const host = instance.current?.getViewport();
      // Diagnostic attributes contain timing/count only, no route metadata.
      const element = document.querySelector('[data-testid="topology-flow"]');
      element?.setAttribute(
        "data-layout-ms",
        String(Math.round(performance.now() - started)),
      );
      element?.setAttribute("data-layout-count", String(layoutCount.current));
      refresh();
      if (firstFit.current) {
        firstFit.current = false;
        requestAnimationFrame(readableOverview);
      } else if (host) instance.current?.setViewport(host);
    })()
      .catch((e) => {
        if (cancelled) return;
        engine.current?.terminateWorker();
        engine.current = null;
        setError(
          `Автоматическая раскладка недоступна: ${String(e.message || e)}. Используется простая сетка.`,
        );
        positions.current = grid();
        refresh();
        if (firstFit.current) {
          firstFit.current = false;
          requestAnimationFrame(readableOverview);
        }
      })
      .finally(() => {
        clearTimeout(timer);
        clearTimeout(bootTimer);
        if (!cancelled) setBusy(false);
      });
    return () => {
      cancelled = true;
      clearTimeout(timer);
      clearTimeout(bootTimer);
    };
    // Structure is intentionally the complete layout input, not live metrics.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [structure, retry, resetKey]);

  function refresh() {
    setNodes((previous) => {
      const old = new Map(previous.map((n) => [n.id, n]));
      return latest.current.nodes.map((n) => ({
        id: n.id,
        type: "route" as const,
        parentId: n.parentId,
        extent: n.parentId ? ("parent" as const) : undefined,
        position: positions.current.get(n.id) ||
          old.get(n.id)?.position || { x: 0, y: 0 },
        draggable: !pinned.has(n.id),
        selected: selected?.id === n.id,
        width: n.parentId ? (mobile ? 316 : 456) : dimensions.get(n.id)?.width,
        height: n.parentId
          ? mobile
            ? 252
            : 164
          : dimensions.get(n.id)?.height,
        style: { opacity: focus && selected && !related.has(n.id) ? 0.22 : 1 },
        ariaLabel: `${n.type}: ${n.label}`,
        ariaRole: "button",
        data: {
          model: n,
          active: n.members?.reduce((v, r) => v + r.active, 0) || 0,
          direction,
          mobile,
          choose,
          toggle: toggleGroup,
        },
      }));
    });
  }
  useEffect(refresh, [graph, selected, pinned, focus, direction, related]);
  const edges = graph.edges.map((e, i) => {
    const a = graph.nodes.find((n) => n.id === e.from),
      b = graph.nodes.find((n) => n.id === e.to);
    const dim =
      focus && selected && (!related.has(e.from) || !related.has(e.to));
    return {
      id: `${e.from}>${e.to}:${i}`,
      source: e.from,
      target: e.to,
      type: "smoothstep",
      animated: false,
      style: {
        stroke: e.configured
          ? theme.palette.text.disabled
          : theme.palette.primary.main,
        strokeWidth: 1.5,
        strokeDasharray: e.configured ? "5 5" : undefined,
        opacity: dim ? 0.18 : 1,
      },
      ariaLabel: `${e.configured ? "Настроенная связь" : "Наблюдаемая связь"} ${a?.label} → ${b?.label}`,
      data: { model: e },
      selectable: true,
    };
  });
  return (
    <Paper
      variant="outlined"
      sx={{
        overflow: "hidden",
        display: "flex",
        flexDirection: "column",
        flex: fullscreen ? 1 : undefined,
        minHeight: 0,
      }}
    >
      <Stack
        direction="row"
        useFlexGap
        flexWrap="wrap"
        gap={1}
        p={1}
        alignItems="center"
      >
        <Tooltip
          describeChild
          title="Показать всю схему. Для чтения вернитесь к масштабу 100%."
        >
          <Button
            onClick={() =>
              void instance.current?.fitView({
                padding: 0.15,
                duration: reduced ? 0 : 250,
                minZoom: 0.15,
                maxZoom: 1,
              })
            }
          >
            Вместить
          </Button>
        </Tooltip>
        <Button
          onClick={() => {
            const v = instance.current;
            if (!v) return;
            const host = v.getViewport();
            const box = document
              .querySelector('[data-testid="topology-flow"]')
              ?.getBoundingClientRect();
            if (box)
              void v.setCenter(
                (box.width / 2 - host.x) / host.zoom,
                (box.height / 2 - host.y) / host.zoom,
                { zoom: 1, duration: reduced ? 0 : 250 },
              );
          }}
        >
          100%
        </Button>
        <Button
          disabled={!selected}
          onClick={() => {
            if (selected)
              void instance.current?.fitView({
                nodes: [{ id: selected.id }],
                padding: 0.5,
                minZoom: 1,
                maxZoom: 1,
                duration: reduced ? 0 : 250,
              });
          }}
        >
          К выбранному
        </Button>
        <Button aria-pressed={focus} onClick={() => setFocus(!focus)}>
          Фокус маршрута
        </Button>
        <Button
          disabled={busy}
          onClick={() => {
            firstFit.current = true;
            setRetry((v) => v + 1);
          }}
        >
          Перестроить
        </Button>
        <Chip
          size="small"
          variant="outlined"
          sx={{ display: fullscreen && mobile ? "none" : undefined }}
          label={busy ? "Расстановка узлов…" : "React Flow · ELK"}
        />
        <Typography
          variant="body2"
          color="text.secondary"
          sx={{ display: fullscreen && mobile ? "none" : undefined }}
        >
          Сплошная - наблюдение · пунктир - конфигурация
        </Typography>
      </Stack>
      {error && <Alert severity="warning">{error}</Alert>}
      <Box
        data-testid="topology-flow"
        role="group"
        aria-label="Интерактивная карта трафика"
        sx={{
          height: fullscreen ? undefined : mobile ? 520 : 720,
          flex: fullscreen ? 1 : undefined,
          minHeight: fullscreen ? 160 : 400,
          bgcolor: "background.default",
        }}
      >
        <ReactFlow<CardNode>
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          onInit={(v) => {
            instance.current = v;
          }}
          onNodesChange={(changes) =>
            setNodes((old) => applyNodeChanges(changes, old))
          }
          onNodeDragStop={(_, n) => positions.current.set(n.id, n.position)}
          onNodeClick={(_, n) => choose(n.data.model)}
          onEdgeClick={(_, e) => {
            const n = graph.nodes.find((n) => n.id === e.target);
            if (n) choose(n);
          }}
          onSelectionChange={handleSelection}
          nodesConnectable={false}
          edgesReconnectable={false}
          deleteKeyCode={null}
          minZoom={0.1}
          maxZoom={2}
          colorMode={theme.palette.mode}
          onlyRenderVisibleElements={false}
          panOnScroll={false}
          preventScrolling={false}
          defaultEdgeOptions={{ interactionWidth: 20 }}
        >
          <Background gap={22} size={1} />
          <Controls showInteractive={false} />
          {!mobile && (
            <MiniMap
              position="bottom-left"
              style={{ width: 140, height: 90, left: 48 }}
              pannable
              zoomable
              nodeColor={theme.palette.primary.light}
              maskColor={theme.palette.action.hover}
            />
          )}
        </ReactFlow>
      </Box>
    </Paper>
  );
}
