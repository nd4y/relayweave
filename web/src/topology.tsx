import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  Collapse,
  ThemeProvider,
  createTheme,
  useTheme,
  Drawer,
  FormControlLabel,
  MenuItem,
  Paper,
  Stack,
  Switch,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  TextField,
  Typography,
  useMediaQuery,
} from "@mui/material";
import { api, message } from "./api";
import "./topology.css";
import { TopologyCanvas } from "./topology-canvas";
import { routeHistories, type TrafficPoint } from "./topology-series";
import { ipGroups } from "./topology-groups";

type Candidate = {
  route: string;
  engine: string;
  active: number;
  score?: number;
  reason: string;
  group: string;
  speed_bps: number;
  measurement_at: number;
  factors?: string[];
  effective_rtt_ms?: number;
  consecutive_failures?: number;
  error_ewma?: number;
  speed_used_bps?: number;
  speed_source?: string;
  passive_at?: number;
  ceiling_bps?: number;
  formula?: string;
};
type Change = {
  id: string;
  worker: string;
  kind: string;
  route?: string;
  engine?: string;
  before: string;
  after: string;
  at: string;
  previous_at: string;
  generation: number;
  source: string;
};
type Decision = {
  observation?: Change;
  flow_id?: string;
  epoch?: string;
  worker?: string;
  engine?: string;
  id: string;
  at: string;
  finished_at?: string;
  generation: number;
  policy: string;
  mode: string;
  method: string;
  listener: string;
  protocol: string;
  selected: string;
  attempted?: string;
  reason: string;
  candidates: Candidate[];
  truncated: boolean;
  prepared_at: string;
};
type Route = {
  id: string;
  engine: string;
  core: string;
  version: string;
  build_sha256?: string;
  subscription: string;
  server_host: string;
  server_ips: string[];
  dns_at: string;
  group: string;
  state: string;
  active: number;
  connections: number;
  failures: number;
  up: number;
  down: number;
  speed_bps: number;
  measurement_at: number;
  rtt_us: number;
  valid_until: string;
};
export type Frame = {
  changes?: Change[];
  worker: string;
  epoch: string;
  sequence: number;
  at: string;
  finished_at?: string;
  generation: number;
  catalog_size: number;
  routes: Route[];
  listeners: { id: string; protocol: string; policy: string }[];
  decisions: Decision[];
  sampled: number;
  sample_every: number;
  dropped: number;
  udp_active: number;
  lease_until: number;
  truncated: boolean;
  prepared_at: string;
};
type Snapshot = {
  schema: number;
  server_time: string;
  cursor: string;
  workers: Frame[];
  completeness: string;
  limitations: string[];
  retention: Record<string, number>;
};
export type Row = Route & {
  worker: string;
  epoch: string;
  at: string;
  finished_at?: string;
  generation: number;
  upMbps: number | null;
  downMbps: number | null;
  newRate: number | null;
  errorRate: number | null;
  window: number;
  history?: TrafficPoint[];
};
export type Node = {
  id: string;
  label: string;
  type: string;
  lane: number;
  parentId?: string;
  childIndex?: number;
  expanded?: boolean;
  row?: Row;
  members?: Row[];
};
export type Edge = {
  from: string;
  to: string;
  configured?: boolean;
  row?: Row;
  members?: Row[];
};
const reason: Record<string, string> = {
  selected_by_policy: "Выбран действующей политикой",
  target_connect_failed: "Не удалось установить транспортное соединение",
  probe_failed: "Проверка назначения политики не пройдена",
  early_transport_close: "Туннель рано закрыт без ответа; причина неизвестна",
  target_cooldown: "Пауза повторов для назначения",
  core_not_ready: "Ядро не готово",
  subscription_expired: "Истёк срок профиля",
  route_revoked: "Маршрут отозван",
  circuit_open: "Circuit breaker открыт",
  eligible_retry_order: "Допустимая альтернатива в порядке повторов",
  compared: "Участвовал в фактическом сравнении",
  warm_reserve: "Подготовленный резерв",
  prepared_active_path: "Подготовленный активный путь",
  excluded_by_manual_pin: "Исключён ручным закреплением",
  not_in_prepared_window: "Вне подготовленного окна выбора",
  not_returned_by_selector: "Не возвращён алгоритмом выбора",
  lease_expired: "Истекла аренда",
  dns_unknown: "DNS неизвестен; независимость IP не установлена",
  max_connections: "Достигнут лимит соединений",
  distinct_server_ip_preferred: "Предпочтён отдельный серверный IP",
  shared_balancer_deprioritized: "Общий IP отложен после отдельных путей",
  insufficient_observation: "Недостаточно наблюдений",
};
const metric = (n: number | null, digits = 2) =>
  n === null
    ? "нет данных"
    : n.toLocaleString("ru-RU", { maximumFractionDigits: digits });
const at = (s: string) => new Date(s).toLocaleTimeString("ru-RU");
const sequence = (s: string) => Number(s.split(":")[1]);

export function TrafficTopology({
  navigate,
}: {
  navigate: (s: string) => void;
}) {
  const [data, setData] = useState<Snapshot | null>(null),
    [paused, setPaused] = useState(false),
    [status, setStatus] = useState("loading"),
    [error, setError] = useState("");
  const [view, setView] = useState("traffic"),
    [aggregation, setAggregation] = useState("ip"),
    [table, setTable] = useState(false),
    [query, setQuery] = useState(""),
    [worker, setWorker] = useState(""),
    [state, setState] = useState(""),
    [subscription, setSubscription] = useState(""),
    [listener, setListener] = useState(""),
    [protocol, setProtocol] = useState(""),
    [core, setCore] = useState(""),
    [direction, setDirection] = useState("both"),
    [errorsOnly, setErrorsOnly] = useState(false),
    [windowSec, setWindowSec] = useState(10);
  const [selection, setSelected] = useState<Node | null>(null),
    [decision, setDecision] = useState<Decision | null>(null),
    [tick, setTick] = useState(Date.now()),
    [history, setHistory] = useState<any[] | null>(null);
  const [pinned, setPinned] = useState<Set<string>>(new Set()),
    [expanded, setExpanded] = useState(false),
    [expandedGroups, setExpandedGroups] = useState<Set<string>>(new Set()),
    [groupPage, setGroupPage] = useState(0),
    [resetKey, setResetKey] = useState(0);
  const mapRoot = useRef<HTMLDivElement>(null);
  const outerTheme = useTheme();
  const mapTheme = useMemo(
    () =>
      createTheme(outerTheme, {
        components: {
          MuiPopover: { defaultProps: { container: () => mapRoot.current } },
          MuiPopper: { defaultProps: { container: () => mapRoot.current } },
          MuiModal: { defaultProps: { container: () => mapRoot.current } },
        },
      }),
    [outerTheme],
  );
  const reducedMotion = useMediaQuery("(prefers-reduced-motion: reduce)");
  const [filtersOpen, setFiltersOpen] = useState(true);
  const [fullscreen, setFullscreen] = useState(false);
  const activeFilters = [
    query,
    worker,
    state,
    subscription,
    listener,
    protocol,
    core,
    errorsOnly,
    direction !== "both",
  ].filter(Boolean).length;
  useEffect(() => {
    const sync = () =>
      setFullscreen(document.fullscreenElement === mapRoot.current);
    document.addEventListener("fullscreenchange", sync);
    return () => document.removeEventListener("fullscreenchange", sync);
  }, []);
  useEffect(() => {
    if (!fullscreen) return;
    const overflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    const escape = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      if (document.fullscreenElement === mapRoot.current)
        void document.exitFullscreen();
      else setFullscreen(false);
    };
    document.addEventListener("keydown", escape);
    return () => {
      document.body.style.overflow = overflow;
      document.removeEventListener("keydown", escape);
    };
  }, [fullscreen]);
  const toggleFullscreen = async () => {
    if (fullscreen) {
      if (document.fullscreenElement === mapRoot.current)
        await document.exitFullscreen();
      else setFullscreen(false);
    } else {
      try {
        if (!mapRoot.current?.requestFullscreen) throw new Error("unsupported");
        await mapRoot.current.requestFullscreen();
      } catch {
        // A full-window fallback also works in embedded/unsupported browsers.
        setFullscreen(true);
      }
    }
  };
  const toggleGroup = useCallback(
    (id: string) =>
      setExpandedGroups((prev) => {
        const next = new Set(prev);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        return next;
      }),
    [],
  );
  const samples = useRef<Map<string, Frame[]>>(new Map()),
    cursor = useRef(""),
    visible = useRef(!document.hidden);
  const mobile = useMediaQuery("(max-width: 800px)");
  const receive = useCallback((s: Snapshot) => {
    cursor.current = s.cursor;
    for (const f of s.workers) {
      const key = f.worker + ":" + f.epoch;
      const list = samples.current.get(key) || [];
      if (
        !list.length ||
        (list[list.length - 1].sequence < f.sequence &&
          Date.parse(list[list.length - 1].at) < Date.parse(f.at))
      ) {
        list.push({ ...f, decisions: [], changes: [] });
        while (list.length > 65) list.shift();
        samples.current.set(key, list);
        while (samples.current.size > 32)
          samples.current.delete(samples.current.keys().next().value!);
      }
    }
    setData(s);
  }, []);
  useEffect(() => {
    const timer = setInterval(() => {
      if (!document.hidden) setTick(Date.now());
    }, 1000);
    const change = () => {
      visible.current = !document.hidden;
    };
    document.addEventListener("visibilitychange", change);
    return () => {
      clearInterval(timer);
      document.removeEventListener("visibilitychange", change);
    };
  }, []);
  useEffect(() => {
    if (paused) return;
    let closed = false,
      stream: EventSource | null = null,
      timer: ReturnType<typeof setTimeout> | undefined,
      delay = 1000,
      attempt = 0,
      current: Snapshot | null = null;
    const reconnect = () => {
      attempt++;
      stream?.close();
      clearTimeout(timer);
      if (closed) return;
      setStatus("reconnecting");
      timer = setTimeout(start, delay);
      delay = Math.min(delay * 2, 30000);
    };
    async function start() {
      const own = ++attempt;
      try {
        const s = await api<Snapshot>("topology/snapshot");
        if (closed || own !== attempt) return;
        current = s;
        receive(s);
        setStatus("live");
        setError("");
        stream = new EventSource(
          "/api/v1/topology/events?cursor=" + encodeURIComponent(s.cursor),
        );
        stream.addEventListener("frame", (e) => {
          const m = e as MessageEvent;
          const event = JSON.parse(m.data) as {
            cursor: string;
            worker: string;
            frame: Frame;
          };
          if (!current) return;
          if (event.cursor === cursor.current) return;
          if (
            event.cursor.split(":")[0] !== cursor.current.split(":")[0] ||
            sequence(event.cursor) !== sequence(cursor.current) + 1
          ) {
            reconnect();
            return;
          }
          current = {
            ...current,
            cursor: event.cursor,
            workers: [
              ...current.workers.filter((f) => f.worker !== event.worker),
              event.frame,
            ],
          };
          cursor.current = event.cursor;
          if (visible.current) {
            receive(current);
          }
          delay = 1000;
          setStatus("live");
        });
        stream.addEventListener("heartbeat", () => {
          if (current && visible.current) receive(current);
        });
        stream.addEventListener("resync", reconnect);
        stream.onerror = reconnect;
      } catch (e) {
        if (closed || own !== attempt) return;
        setError(message(e));
        if ((e as any).status === 401 || (e as any).status === 403) {
          setStatus("permission-error");
          return;
        }
        reconnect();
      }
    }
    const online = () => {
      delay = 1000;
      reconnect();
    };
    window.addEventListener("offline", reconnect);
    window.addEventListener("online", online);
    void start();
    return () => {
      closed = true;
      window.removeEventListener("offline", reconnect);
      window.removeEventListener("online", online);
      stream?.close();
      clearTimeout(timer);
    };
  }, [paused, receive]);
  const rows = useMemo<Row[]>(
    () =>
      data?.workers.flatMap((f) => {
        const frames = samples.current.get(f.worker + ":" + f.epoch) || [];
        const histories = routeHistories(frames, windowSec);
        return f.routes.map((r) => {
          const list = samples.current.get(f.worker + ":" + f.epoch) || [];
          const base =
            [...list]
              .reverse()
              .find(
                (v) => Date.parse(f.at) - Date.parse(v.at) >= windowSec * 1000,
              ) || list[0];
          const old = base?.routes.find((v) => v.engine === r.engine);
          const elapsed = base
            ? (Date.parse(f.at) - Date.parse(base.at)) / 1000
            : 0;
          const rate = (key: "up" | "down" | "connections" | "failures") =>
            old && elapsed > 0 && r[key] >= old[key]
              ? (r[key] - old[key]) / elapsed
              : null;
          const up = rate("up"),
            down = rate("down");
          return {
            ...r,
            worker: f.worker,
            epoch: f.epoch,
            at: f.at,
            generation: f.generation,
            upMbps: up === null ? null : (up * 8) / 1e6,
            downMbps: down === null ? null : (down * 8) / 1e6,
            newRate: rate("connections"),
            errorRate: rate("failures"),
            window: elapsed,
            history: histories.get(r.engine),
          };
        });
      }) || [],
    [data, windowSec],
  );
  const filtered = rows.filter(
    (r) =>
      (!worker || r.worker === worker) &&
      (!state || r.state === state) &&
      (!subscription || r.subscription === subscription) &&
      (!core || r.version === core) &&
      (!errorsOnly || r.failures > 0) &&
      (!listener ||
        data?.workers
          .find((f) => f.worker === r.worker)
          ?.listeners.some((l) => l.id === listener)) &&
      (!protocol ||
        data?.workers
          .find((f) => f.worker === r.worker)
          ?.listeners.some((l) => l.protocol === protocol)) &&
      [
        r.id,
        r.worker,
        r.core,
        r.version,
        r.group,
        r.server_host,
        ...r.server_ips,
      ]
        .join(" ")
        .toLowerCase()
        .includes(query.toLowerCase()),
  );
  const top = filtered
    .slice()
    .sort(
      (a, b) =>
        Number(b.active > 0) - Number(a.active > 0) ||
        (a.worker + a.engine).localeCompare(b.worker + b.engine),
    )
    .slice(0, 60);
  const overviewGroups = ipGroups(top).sort(
    (a, b) =>
      Number(b.members.some((r) => r.active > 0)) -
        Number(a.members.some((r) => r.active > 0)) || a.id.localeCompare(b.id),
  );
  const pageCount = Math.max(1, Math.ceil(overviewGroups.length / 4));
  const safePage = Math.min(groupPage, pageCount - 1);
  const graph = useMemo(() => {
    const nodes = new Map<string, Node>(),
      edges: Edge[] = [];
    const add = (
      id: string,
      label: string,
      type: string,
      lane: number,
      row?: Row,
    ) => {
      const old = nodes.get(id);
      if (old) {
        if (
          row &&
          !old.members?.some(
            (r) => r.worker === row.worker && r.engine === row.engine,
          )
        )
          old.members?.push(row);
      } else
        nodes.set(id, {
          id,
          label,
          type,
          lane,
          row,
          members: row ? [row] : [],
        });
      return id;
    };
    const link = (from: string, to: string, row?: Row, configured = false) => {
      const old = edges.find((e) => e.from === from && e.to === to);
      if (old) {
        if (
          row &&
          !old.members?.some(
            (r) => r.worker === row.worker && r.engine === row.engine,
          )
        )
          old.members?.push(row);
      } else
        edges.push({ from, to, row, configured, members: row ? [row] : [] });
    };
    if (decision && view === "traffic") {
      const route =
        decision.observation?.route ||
        decision.selected ||
        decision.attempted ||
        "маршрут неизвестен";
      const candidate = decision.candidates?.find((c) => c.route === route);
      let path: [string, string, string][] = [
        ["decision-clients", "Клиент неизвестен", "сохранённый путь"],
        ["decision-listener", decision.listener || "вход неизвестен", "вход"],
        ["decision-worker", decision.worker || "воркер неизвестен", "воркер"],
        [
          "decision-engine",
          decision.engine || "ядро неизвестно",
          "экземпляр ядра",
        ],
        [
          "decision-route",
          route,
          decision.selected ? "выбранный профиль" : "попытка",
        ],
        [
          "decision-group",
          candidate?.group || "группа неизвестна",
          "IP-группа на момент выбора",
        ],
        ["decision-target", "Назначение неизвестно", "назначения"],
      ];
      if (decision.observation) {
        path = [
          [
            "decision-worker",
            decision.worker || "неизвестен",
            "наблюдаемый воркер",
          ],
        ];
        if (decision.engine)
          path.push(["decision-engine", decision.engine, "наблюдаемое ядро"]);
        if (decision.observation.route)
          path.push([
            "decision-route",
            decision.observation.route,
            "наблюдаемый профиль",
          ]);
      }
      path.forEach(([id, label, type], index) => add(id, label, type, index));
      for (let i = 1; i < path.length; i++)
        link(path[i - 1][0], path[i][0], undefined, !!decision.observation);
      return { nodes: [...nodes.values()], edges };
    }
    if (view === "traffic" && aggregation === "ip") {
      for (const g of overviewGroups.slice(safePage * 4, safePage * 4 + 4)) {
        const first = g.members[0];
        const group: Node = {
          id: g.id,
          label:
            g.ips.join(", ") ||
            `IP неизвестен · ${first.server_host || first.id.slice(0, 8)}`,
          type: "группа серверных IP",
          lane: 3,
          row: first,
          members: g.members,
          expanded: expandedGroups.has(g.id),
        };
        nodes.set(g.id, group);
        for (const r of g.members) {
          const w = add(r.worker + ":worker", r.worker, "воркер", 2, r);
          const client = add(
            "clients",
            "Клиенты (агрегат)",
            "идентичность неизвестна",
            0,
          );
          const inputs = (
            data?.workers.find((f) => f.worker === r.worker)?.listeners || []
          ).filter(
            (l) =>
              (!listener || l.id === listener) &&
              (!protocol || l.protocol === protocol),
          );
          const n = add(
            r.worker + ":inputs",
            [...new Set(inputs.map((l) => l.protocol.toUpperCase()))].join(
              " / ",
            ) + ` · ${inputs.length}`,
            "точки входа",
            1,
          );
          link(client, n, undefined, true);
          link(n, w, undefined, true);
          link(w, g.id, r);
        }
        if (group.expanded)
          g.members.forEach((r, i) => {
            const id = "profile:" + r.worker + ":" + r.engine;
            nodes.set(id, {
              id,
              label: r.id,
              type: "профиль",
              lane: 4,
              row: r,
              members: [r],
              parentId: g.id,
              childIndex: i,
            });
          });
      }
      return { nodes: [...nodes.values()], edges };
    }
    for (const r of top) {
      const base = r.worker + ":";
      if (view === "dependencies") {
        const source = add(
            "source:" + r.subscription,
            r.subscription,
            "подписка",
            0,
            r,
          ),
          profile = add(base + r.id, r.id, "профиль", 1, r),
          host = add(
            "host:" + r.server_host,
            r.server_host || "DNS неизвестен",
            "сервер",
            2,
            r,
          ),
          group = add(
            base + "group:" + r.group,
            r.group,
            "общий балансир (предположение)",
            4,
            r,
          );
        link(source, profile, undefined, true);
        link(profile, host, undefined, true);
        if (r.server_ips.length) {
          for (const ip of r.server_ips) {
            const node = add("ip:" + ip, ip, "наблюдаемый A/AAAA", 3, r);
            link(host, node, undefined, true);
            link(node, group, undefined, true);
          }
        } else {
          link(host, group, undefined, true);
        }
      } else {
        const clients = add(
            "clients",
            "Клиенты (агрегат)",
            "идентичность неизвестна",
            0,
          ),
          w = add(base + "worker", r.worker, "воркер", 2, r),
          engine = add(base + r.engine, r.version, "экземпляр Xray", 3, r),
          profile = add(
            base +
              "aggregate:" +
              aggregation +
              ":" +
              (aggregation === "worker"
                ? r.worker
                : aggregation === "core"
                  ? r.core
                  : aggregation === "subscription"
                    ? r.subscription
                    : aggregation === "ip"
                      ? r.group
                      : r.id),
            aggregation === "worker"
              ? r.worker
              : aggregation === "core"
                ? r.version
                : aggregation === "subscription"
                  ? r.subscription
                  : aggregation === "ip"
                    ? r.group
                    : r.id,
            aggregation === "profile" ? "профиль" : "группа " + aggregation,
            4,
            r,
          ),
          group = add(
            base + "group:" + r.group,
            r.server_ips.join(", ") || "DNS неизвестен",
            "группа серверных IP",
            5,
            r,
          ),
          target = add(
            "target",
            "Назначения (агрегат)",
            "IP / приложение неизвестны",
            6,
          );
        for (const l of data?.workers.find((f) => f.worker === r.worker)
          ?.listeners || []) {
          if (
            (listener && l.id !== listener) ||
            (protocol && l.protocol !== protocol)
          )
            continue;
          const n = add(base + "listener:" + l.id, l.id, l.protocol, 1);
          link(clients, n, undefined, true);
          link(n, w, undefined, true);
        }
        link(w, engine, r);
        link(engine, profile, r);
        link(profile, group, r);
        link(group, target, r);
      }
    }
    return {
      nodes: [...nodes.values()].slice(0, 500),
      edges: edges.slice(0, 1000),
    };
  }, [
    data,
    decision,
    view,
    aggregation,
    expandedGroups,
    safePage,
    listener,
    protocol,
    top.map((r) => JSON.stringify(r)).join("|"),
  ]);
  const selected = selection
    ? graph.nodes.find((n) => n.id === selection.id) || selection
    : null;
  const related = new Set<string>();
  if (selected) {
    related.add(selected.id);
    const members = selected.members?.length
      ? selected.members
      : selected.row
        ? [selected.row]
        : [];
    const keys = new Set(members.map((r) => r.worker + ":" + r.engine));
    for (const n of graph.nodes) {
      if (
        decision ||
        n.members?.some((r) => keys.has(r.worker + ":" + r.engine)) ||
        n.id === "clients" ||
        n.id === "target"
      )
        related.add(n.id);
    }
    for (const e of graph.edges) {
      if (related.has(e.to) && e.configured) related.add(e.from);
      if (!members.length && (e.from === selected.id || e.to === selected.id)) {
        related.add(e.from);
        related.add(e.to);
      }
    }
  }
  const stale = data?.workers.some((f) => tick - Date.parse(f.at) > 5000),
    decisions =
      data?.workers
        .flatMap((f) => f.decisions.map((d) => ({ ...d, worker: f.worker })))
        .sort((a, b) => Date.parse(b.at) - Date.parse(a.at)) || [];
  const openDecision = async (d: Decision) => {
    setDecision(d);
    setView("traffic");
    setTable(false);
    setSelected({
      id: "decision-route",
      label: d.selected || d.attempted || "Сохранённое решение",
      type: "сохранённое решение",
      lane: 4,
    });
    try {
      setDecision(
        await api<Decision>("topology/decisions/" + encodeURIComponent(d.id)),
      );
    } catch (e) {
      setError(message(e));
    }
  };
  const observedChanges =
    data?.workers
      .flatMap((f) => f.changes || [])
      .sort((a, b) => Date.parse(b.at) - Date.parse(a.at)) || [];
  const openChange = (c: Change) => {
    setView("traffic");
    setTable(false);
    setSelected({
      id: c.route
        ? "decision-route"
        : c.engine
          ? "decision-engine"
          : "decision-worker",
      label: c.kind,
      type: "наблюдаемое изменение",
      lane: 0,
    });
    setDecision({
      id: c.id,
      at: c.at,
      worker: c.worker,
      engine: c.engine,
      generation: c.generation,
      policy: "",
      mode: "",
      method: c.source,
      listener: "",
      protocol: "",
      selected: "",
      reason: c.kind,
      candidates: [],
      truncated: false,
      prepared_at: c.previous_at,
      observation: c,
    });
  };
  const choose = (n: Node) => {
    setSelected(n);
    setExpanded(false);
    if (!decision) setDecision(null);
  };
  const choices = (values: string[]) =>
    ["", ...new Set(values)].map((v) => (
      <MenuItem key={v} value={v}>
        {v || "Все"}
      </MenuItem>
    ));
  const reset = () => {
    setResetKey((v) => v + 1);
    setExpandedGroups(new Set());
    setGroupPage(0);
    setSelected(null);
  };
  return (
    <ThemeProvider theme={mapTheme}>
      <Stack
        ref={mapRoot}
        spacing={2}
        data-testid="traffic-topology"
        className={fullscreen ? "rw-topology-fullscreen" : undefined}
        sx={
          fullscreen
            ? {
                position: "fixed",
                inset: 0,
                zIndex: 1400,
                height: "100dvh",
                p: { xs: 1, md: 2 },
                bgcolor: "background.default",
                overflow: "hidden",
              }
            : undefined
        }
      >
        <Paper
          className="rw-topology-filters"
          variant="outlined"
          sx={{
            p: 2,
            flexShrink: 0,
            maxHeight: fullscreen ? "40dvh" : undefined,
            overflowY: "auto",
          }}
        >
          <Stack
            direction="row"
            useFlexGap
            flexWrap="wrap"
            gap={1}
            alignItems="center"
          >
            <Chip
              color={
                paused
                  ? "default"
                  : stale || status !== "live"
                    ? "warning"
                    : "success"
              }
              label={
                paused
                  ? "Пауза"
                  : stale
                    ? "Устаревшие данные"
                    : status === "live"
                      ? "Live"
                      : status === "loading"
                        ? "Загрузка"
                        : "Переподключение"
              }
              aria-live="polite"
            />
            <Button variant="outlined" onClick={() => setPaused(!paused)}>
              {paused ? "Продолжить" : "Пауза"}
            </Button>
            <Button
              onClick={() => setFiltersOpen(!filtersOpen)}
              aria-expanded={filtersOpen}
              aria-controls="topology-filters"
              aria-label={`${filtersOpen ? "Свернуть фильтры" : "Показать фильтры"}${activeFilters ? ` (${activeFilters})` : ""}`}
            >
              {mobile
                ? "Фильтры"
                : filtersOpen
                  ? "Свернуть фильтры"
                  : "Показать фильтры"}
              {activeFilters ? ` (${activeFilters})` : ""}
            </Button>
            <Button
              aria-label={
                fullscreen ? "Выйти из полного экрана" : "Полный экран"
              }
              aria-pressed={fullscreen}
              onClick={() => void toggleFullscreen()}
            >
              {fullscreen
                ? mobile
                  ? "Обычный вид"
                  : "Выйти из полного экрана"
                : "Полный экран"}
            </Button>
          </Stack>
          <Collapse in={filtersOpen} timeout={reducedMotion ? 0 : "auto"}>
            <Box id="topology-filters" sx={{ pt: 2 }}>
              <Stack
                direction="row"
                useFlexGap
                flexWrap="wrap"
                gap={1}
                alignItems="center"
              >
                <TextField
                  select
                  size="small"
                  label="Представление"
                  value={view}
                  onChange={(e) => {
                    setView(e.target.value);
                    setDecision(null);
                    setSelected(null);
                  }}
                  sx={{ minWidth: 190 }}
                >
                  <MenuItem value="traffic">Трафик</MenuItem>
                  <MenuItem value="dependencies">Подписки и пути</MenuItem>
                  <MenuItem value="events">Решения и события</MenuItem>
                </TextField>
                <TextField
                  select
                  size="small"
                  label="Агрегирование"
                  value={aggregation}
                  onChange={(e) => {
                    setAggregation(e.target.value);
                    setSelected(null);
                  }}
                  sx={{ minWidth: 150 }}
                >
                  {[
                    ["profile", "Профиль"],
                    ["worker", "Воркер"],
                    ["core", "Ядро"],
                    ["ip", "Серверный IP"],
                    ["subscription", "Подписка"],
                  ].map(([id, label]) => (
                    <MenuItem key={id} value={id}>
                      {label}
                    </MenuItem>
                  ))}
                </TextField>
                <TextField
                  select
                  size="small"
                  label="Окно скорости"
                  value={windowSec}
                  onChange={(e) => setWindowSec(Number(e.target.value))}
                >
                  {[1, 10, 30, 60].map((n) => (
                    <MenuItem key={n} value={n}>
                      {n} с
                    </MenuItem>
                  ))}
                </TextField>
                <TextField
                  size="small"
                  label="Поиск профиля / IP"
                  value={query}
                  onChange={(e) => setQuery(e.target.value.slice(0, 100))}
                />
                <FormControlLabel
                  control={
                    <Switch
                      checked={table}
                      onChange={(e) => {
                        setTable(e.target.checked);
                        setDecision(null);
                        setSelected(null);
                      }}
                    />
                  }
                  label="Таблица"
                />
                <Button onClick={reset}>Сбросить вид</Button>
              </Stack>
              <Stack direction="row" useFlexGap flexWrap="wrap" gap={1} mt={2}>
                <TextField
                  select
                  size="small"
                  label="Воркер"
                  value={worker}
                  onChange={(e) => setWorker(e.target.value)}
                  sx={{ minWidth: 140 }}
                >
                  {choices(rows.map((r) => r.worker))}
                </TextField>
                <TextField
                  select
                  size="small"
                  label="Состояние"
                  value={state}
                  onChange={(e) => setState(e.target.value)}
                  sx={{ minWidth: 150 }}
                >
                  {choices(rows.map((r) => r.state))}
                </TextField>
                <TextField
                  select
                  size="small"
                  label="Подписка"
                  value={subscription}
                  onChange={(e) => setSubscription(e.target.value)}
                  sx={{ minWidth: 140 }}
                >
                  {choices(rows.map((r) => r.subscription))}
                </TextField>
                <TextField
                  select
                  size="small"
                  label="Ядро / версия"
                  value={core}
                  onChange={(e) => setCore(e.target.value)}
                  sx={{ minWidth: 150 }}
                >
                  {choices(rows.map((r) => r.version))}
                </TextField>
                <TextField
                  select
                  size="small"
                  label="Listener"
                  value={listener}
                  onChange={(e) => setListener(e.target.value)}
                  sx={{ minWidth: 130 }}
                >
                  {choices(
                    data?.workers.flatMap((f) =>
                      f.listeners.map((l) => l.id),
                    ) || [],
                  )}
                </TextField>
                <TextField
                  select
                  size="small"
                  label="Протокол"
                  value={protocol}
                  onChange={(e) => setProtocol(e.target.value)}
                  sx={{ minWidth: 130 }}
                >
                  {choices(
                    data?.workers.flatMap((f) =>
                      f.listeners.map((l) => l.protocol),
                    ) || [],
                  )}
                </TextField>
                <TextField
                  select
                  size="small"
                  label="Направление"
                  value={direction}
                  onChange={(e) => setDirection(e.target.value)}
                  sx={{ minWidth: 140 }}
                >
                  <MenuItem value="both">Upload / download</MenuItem>
                  <MenuItem value="up">Upload</MenuItem>
                  <MenuItem value="down">Download</MenuItem>
                </TextField>
                <FormControlLabel
                  control={
                    <Switch
                      checked={errorsOnly}
                      onChange={(e) => setErrorsOnly(e.target.checked)}
                    />
                  }
                  label="С ошибками"
                />
              </Stack>
            </Box>
          </Collapse>
        </Paper>
        {decision && (
          <Alert
            severity="info"
            action={
              <Button
                onClick={() => {
                  setDecision(null);
                  setSelected(null);
                }}
              >
                К живой схеме
              </Button>
            }
          >
            Сохранённый путь {at(decision.at)} · {decision.worker} · поколение{" "}
            {decision.generation}. Граф зафиксирован по фактам решения.
            Исторические байты и неизвестные детали не восстанавливаются из
            текущих метрик.
          </Alert>
        )}
        {(listener || protocol) && (
          <Alert severity="info">
            Фильтр входа ограничивает настроенные связи и события. Счётчики
            маршрутов остаются суммарными по входам и TCP/UDP.
          </Alert>
        )}
        {error && (
          <Alert severity="error" onClose={() => setError("")}>
            {error}
          </Alert>
        )}
        <Stack direction="row" useFlexGap flexWrap="wrap" gap={1}>
          {data?.workers.map((f) => (
            <Chip
              key={f.worker}
              variant="outlined"
              label={`${f.worker} · поколение ${f.generation} · ядер ${f.routes.length} / каталог ${f.catalog_size} · ${at(f.at)} · UDP ${f.udp_active} · пропущено ${f.dropped} · не записано ${f.sampled} (1/${f.sample_every})`}
            />
          ))}
        </Stack>
        {(stale || status === "reconnecting") && (
          <Alert severity="warning">
            Последняя схема сохранена. Потеря control plane не означает
            остановку уже установленных туннелей.
          </Alert>
        )}
        <Alert severity="info">
          Наблюдаемые счётчики маршрутов TCP + UDP. Пунктир - настроенная связь,
          не измеренный поток. IP сервера не является exit IP. Приложения, HTTPS
          URL и ответы внутри TLS неизвестны.
        </Alert>
        {data && !rows.length && (
          <Alert severity="info">
            Пока нет наблюдений воркера. Настройки не подменяют фактический
            трафик.
          </Alert>
        )}
        {rows.length > 0 && !filtered.length && (
          <Alert severity="info">Нет элементов для выбранных фильтров.</Alert>
        )}
        {filtered.length > top.length && (
          <Alert severity="warning">
            Показаны {top.length} из {filtered.length} профилей. Остальные
            доступны через поиск и таблицу.
          </Alert>
        )}
        {data?.workers.some((f) => f.truncated) && (
          <Alert severity="warning">
            Наблюдение усечено по бюджету. Каталог целиком не загружается в
            граф.
          </Alert>
        )}
        {view === "events" ? (
          <Paper
            className="rw-topology-content"
            variant="outlined"
            sx={{ p: 2, overflow: "auto" }}
          >
            <Typography variant="h6">Фактические решения</Typography>
            <Typography color="text.secondary">
              Часть успешных решений семплируется. Изменение выбора относится к
              новым соединениям. Существующие TCP-туннели не переносятся.
            </Typography>
            {!decisions.length && (
              <Typography>Нет сохранённых решений в текущем кадре.</Typography>
            )}
            {decisions
              .filter(
                (d) =>
                  (!worker || d.worker === worker) &&
                  (!listener || d.listener === listener) &&
                  (!protocol ||
                    (protocol === "http"
                      ? d.protocol.startsWith("http")
                      : d.protocol.startsWith("socks5"))),
              )
              .slice(0, 100)
              .map((d) => (
                <Button
                  key={d.id}
                  data-decision-id={d.id}
                  data-selection-at={d.at}
                  data-finished-at={d.finished_at}
                  fullWidth
                  onClick={() => void openDecision(d)}
                  sx={{
                    justifyContent: "flex-start",
                    textAlign: "left",
                    my: 0.5,
                  }}
                >
                  {at(d.at)} · {d.worker} · {d.listener} ·{" "}
                  {d.selected || d.attempted || "нет маршрута"} ·{" "}
                  {reason[d.reason] || d.reason}
                </Button>
              ))}
            <Typography variant="h6" mt={2}>
              Изменения наблюдаемых состояний
            </Typography>
            <Typography color="text.secondary">
              До 64 последних изменений на воркер. Источник - сравнение
              наблюдённых кадров, а не причина выбора селектора.
            </Typography>
            {observedChanges
              .filter((c) => !worker || c.worker === worker)
              .slice(0, 100)
              .map((c) => (
                <Button
                  key={c.id}
                  fullWidth
                  onClick={() => openChange(c)}
                  sx={{
                    justifyContent: "flex-start",
                    textAlign: "left",
                    my: 0.5,
                  }}
                >
                  {at(c.at)} · {c.worker} · {c.kind} ·{" "}
                  {c.before || "неизвестно"} → {c.after || "неизвестно"}
                </Button>
              ))}
          </Paper>
        ) : table ? (
          <Paper className="rw-topology-content" sx={{ overflow: "auto" }}>
            <Table size="small" aria-label="Табличная карта трафика">
              <TableHead>
                <TableRow>
                  {[
                    "Профиль",
                    "Воркер / ядро",
                    "Состояние",
                    "Активные",
                    "Upload Mbps",
                    "Download Mbps",
                    "Новые / с",
                    "Ошибки / с",
                    "IP-группа",
                  ].map((h) => (
                    <TableCell key={h}>{h}</TableCell>
                  ))}
                </TableRow>
              </TableHead>
              <TableBody>
                {filtered.slice(0, 500).map((r) => (
                  <TableRow key={r.worker + r.engine}>
                    <TableCell>
                      <Button
                        onClick={() =>
                          choose({
                            id: r.worker + ":" + r.id,
                            label: r.id,
                            type: "профиль",
                            lane: 4,
                            row: r,
                          })
                        }
                      >
                        {r.id}
                      </Button>
                    </TableCell>
                    <TableCell>
                      {r.worker} / {r.version}
                    </TableCell>
                    <TableCell>{r.state}</TableCell>
                    <TableCell>{r.active}</TableCell>
                    <TableCell>{metric(r.upMbps)}</TableCell>
                    <TableCell>{metric(r.downMbps)}</TableCell>
                    <TableCell>{metric(r.newRate)}</TableCell>
                    <TableCell>{metric(r.errorRate)}</TableCell>
                    <TableCell>{r.group}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Paper>
        ) : (
          <Stack
            className="rw-topology-graph"
            spacing={1}
            sx={{ flex: fullscreen ? 1 : undefined, minHeight: 0 }}
          >
            {view === "traffic" && aggregation === "ip" && !decision && (
              <Stack
                direction="row"
                useFlexGap
                flexWrap="wrap"
                gap={1}
                alignItems="center"
              >
                <Typography variant="body2">
                  IP-группы {overviewGroups.length ? safePage * 4 + 1 : 0}-
                  {Math.min(safePage * 4 + 4, overviewGroups.length)} из{" "}
                  {overviewGroups.length}
                </Typography>
                <Button
                  aria-label="Предыдущие IP"
                  disabled={safePage === 0}
                  onClick={() => {
                    setGroupPage(safePage - 1);
                    setSelected(null);
                  }}
                >
                  {mobile ? "Назад" : "Предыдущие IP"}
                </Button>
                <Button
                  aria-label="Следующие IP"
                  disabled={safePage >= pageCount - 1}
                  onClick={() => {
                    setGroupPage(safePage + 1);
                    setSelected(null);
                  }}
                >
                  {mobile ? "Далее" : "Следующие IP"}
                </Button>
                <Typography
                  variant="caption"
                  color="text.secondary"
                  sx={{ display: fullscreen && mobile ? "none" : undefined }}
                >
                  Активные группы первыми · поиск и таблица доступны для
                  остальных профилей
                </Typography>
              </Stack>
            )}
            <TopologyCanvas
              graph={graph}
              selected={selected}
              related={related}
              pinned={pinned}
              choose={choose}
              toggleGroup={toggleGroup}
              direction={direction}
              resetKey={resetKey}
              fullscreen={fullscreen}
            />
          </Stack>
        )}
        <Stack direction="row" gap={2}>
          <Button
            onClick={() =>
              void api<any>("topology/history")
                .then((v) => setHistory(v.items))
                .catch((e) => setError(message(e)))
            }
          >
            История агрегатов
          </Button>
          <Typography variant="caption" alignSelf="center">
            Частичная видимость · окно {windowSec} с · история 60 мин / шаг 30 с
            · детали до 15 мин
          </Typography>
        </Stack>
        {history && (
          <Paper sx={{ p: 2 }}>
            <Typography variant="h6">
              Сохранённые агрегаты ({history.length})
            </Typography>
            <Typography>
              История содержит только суммарные счётчики; воспроизведение старых
              маршрутов недоступно.
            </Typography>
            {history.slice(-20).map((h, i) => (
              <Typography key={i} variant="body2">
                {at(h.at)} · {h.worker} · active {h.Active} · ↑ {h.Up} bytes · ↓{" "}
                {h.Down} bytes · generation {h.generation}
              </Typography>
            ))}
            <Button onClick={() => setHistory(null)}>Закрыть историю</Button>
          </Paper>
        )}
        <Drawer
          container={() => mapRoot.current}
          variant={mobile ? "temporary" : "persistent"}
          anchor="right"
          open={!!selected || !!decision}
          onClose={() => {
            setSelected(null);
            setDecision(null);
          }}
          slotProps={{
            paper: {
              sx: {
                width: mobile ? "100%" : 440,
                p: 3,
                overflowWrap: "anywhere",
              },
            },
          }}
        >
          <Stack spacing={2}>
            <Stack direction="row" justifyContent="space-between">
              <Typography variant="h6">
                {selected?.label || "Решение"}
              </Typography>
              <Button
                onClick={() => {
                  setSelected(null);
                  setDecision(null);
                }}
              >
                Закрыть
              </Button>
            </Stack>
            <Chip
              label={selected?.type || "Фактическое решение"}
              sx={{ alignSelf: "flex-start" }}
            />
            {selected && (
              <>
                <Button
                  onClick={() =>
                    setPinned((prev) => {
                      const s = new Set(prev);
                      if (s.has(selected.id)) s.delete(selected.id);
                      else s.add(selected.id);
                      return s;
                    })
                  }
                >
                  {pinned.has(selected.id)
                    ? "Открепить"
                    : "Закрепить положение"}
                </Button>
                <Button
                  onClick={() => {
                    if (selected.type === "группа серверных IP")
                      toggleGroup(selected.id);
                    else setExpanded(!expanded);
                  }}
                >
                  {(
                    selected.type === "группа серверных IP"
                      ? selected.expanded
                      : expanded
                  )
                    ? "Свернуть группу"
                    : "Раскрыть группу и соседей"}
                </Button>
              </>
            )}
            {selected?.row && (
              <>
                <Typography>
                  {selected.members && selected.members.length > 1
                    ? `Пример наблюдаемого пути (${selected.members.length} элементов группы):`
                    : `Наблюдаемый путь:`}{" "}
                  клиент (неизвестен) → вход (см. решение) →{" "}
                  {selected.row.worker} → {selected.row.engine} →{" "}
                  {selected.row.id} → {selected.row.group} → назначение
                  (неизвестно)
                </Typography>
                <Typography>
                  Применённое поколение: {selected.row.generation}. Ядро:{" "}
                  {selected.row.version}. Сборка:{" "}
                  {selected.row.build_sha256?.slice(0, 12) || "неизвестна"}.
                  Последнее наблюдение: {at(selected.row.at)}.
                </Typography>
                <Typography>
                  Состояние: {selected.row.state} · активных{" "}
                  {selected.row.active} · ↑ {metric(selected.row.upMbps)} / ↓{" "}
                  {metric(selected.row.downMbps)} Mbps за{" "}
                  {metric(selected.row.window, 1)} с.
                </Typography>
                <Typography>
                  Сервер: {selected.row.server_host || "неизвестен"}. A/AAAA:{" "}
                  {selected.row.server_ips.join(", ") || "неизвестны"}. DNS:{" "}
                  {selected.row.dns_at.startsWith("0001")
                    ? "возраст неизвестен"
                    : at(selected.row.dns_at)}
                  .
                </Typography>
                <Alert severity="info">
                  Общий IP - консервативный признак общей ёмкости. Независимость
                  полосы и публичный exit IP не измерены.
                </Alert>
                <Typography>
                  Проба скорости:{" "}
                  {selected.row.measurement_at
                    ? new Date(
                        selected.row.measurement_at * 1000,
                      ).toLocaleString()
                    : "нет данных"}{" "}
                  {selected.row.measurement_at
                    ? `· ${metric((selected.row.speed_bps * 8) / 1e6)} Mbps.`
                    : ""}{" "}
                  Измерение не доказывает доступную ёмкость сейчас.
                </Typography>
                <Button onClick={() => navigate("profiles")}>
                  Открыть профили
                </Button>
                <Button onClick={() => navigate("sources")}>
                  Открыть подписки
                </Button>
              </>
            )}
            {(expanded || selected?.expanded) && selected && (
              <Box>
                {selected.members?.map((r) => (
                  <Typography variant="body2" key={r.worker + r.engine}>
                    {r.id} · {r.worker} · active {r.active}
                  </Typography>
                ))}
                {graph.nodes
                  .filter((n) => related.has(n.id))
                  .map((n) => (
                    <Button key={n.id} fullWidth onClick={() => choose(n)}>
                      {n.type}: {n.label}
                    </Button>
                  ))}
              </Box>
            )}
            {!decision && selected?.row && (
              <>
                <Typography variant="subtitle1">
                  Последние решения этого пути
                </Typography>
                <Typography variant="body2" color="text.secondary">
                  Причина показывается только для записанного решения. Текущие
                  счётчики сами по себе не объясняют выбор.
                </Typography>
                {!decisions.some(
                  (d) =>
                    d.worker === selected.row?.worker &&
                    (selected.members || [selected.row!]).some(
                      (r) => r.id === d.selected,
                    ),
                ) && (
                  <Alert severity="info">
                    Для этого пути нет сохранённых решений в текущем кадре.
                  </Alert>
                )}
                {decisions
                  .filter((d) =>
                    (selected.members || [selected.row!]).some(
                      (r) => r.worker === d.worker && r.id === d.selected,
                    ),
                  )
                  .slice(0, 12)
                  .map((d) => (
                    <Button key={d.id} onClick={() => void openDecision(d)}>
                      {at(d.at)} · {reason[d.reason] || d.reason}
                    </Button>
                  ))}
              </>
            )}
            {decision && (
              <>
                <Typography variant="h6">
                  {decision.observation
                    ? "Наблюдённое изменение"
                    : "Почему выбран путь"}
                </Typography>
                <Typography>
                  {reason[decision.reason] || decision.reason}
                </Typography>
                <Typography>
                  {at(decision.at)} · generation {decision.generation} · policy{" "}
                  {decision.policy} · {decision.mode} · {decision.method} ·{" "}
                  {decision.protocol}
                </Typography>
                <Typography variant="body2">
                  Worker: {decision.worker || "неизвестен"} · ядро:{" "}
                  {decision.engine || "неизвестно"}.{" "}
                  {decision.observation ? "ID наблюдения" : "ID решения"}:{" "}
                  {decision.id}.{" "}
                  {decision.flow_id ? `Flow ID: ${decision.flow_id}.` : ""}
                </Typography>
                <Typography variant="body2">
                  {decision.observation
                    ? "Изменение наблюдено между кадрами. Смена поколения не означает переноса существующих TCP-туннелей."
                    : "Числовые оценки сохранены только для фактически сравнивавшихся кандидатов. Статус остальных указан отдельно: не все допущены к повторам. Весь каталог не сравнивался."}
                </Typography>
                {decision.observation ? (
                  <Alert severity="info">
                    {decision.observation.before || "Неизвестно"} →{" "}
                    {decision.observation.after || "Неизвестно"}. Наблюдение
                    между кадрами{" "}
                    {decision.observation.previous_at.startsWith("0001")
                      ? "(предыдущего нет)"
                      : at(decision.observation.previous_at)}{" "}
                    и {at(decision.at)}. Точная причина и момент перехода
                    неизвестны; это не записанное решение селектора.
                  </Alert>
                ) : (
                  <Table
                    size="small"
                    aria-label="Альтернативы выбора"
                    sx={{
                      tableLayout: "fixed",
                      "& td, & th": {
                        overflowWrap: "anywhere",
                        verticalAlign: "top",
                      },
                    }}
                  >
                    <TableHead>
                      <TableRow>
                        <TableCell>Профиль</TableCell>
                        <TableCell>Оценка / active</TableCell>
                        <TableCell>Причина</TableCell>
                      </TableRow>
                    </TableHead>
                    <TableBody>
                      {decision.candidates?.map((c, i) => (
                        <TableRow key={i}>
                          <TableCell>
                            {c.route}
                            {c.route === decision.selected ? " ✓" : ""}
                            <br />
                            {c.group}
                          </TableCell>
                          <TableCell>
                            {c.score === undefined
                              ? "не сравнивался"
                              : metric(c.score, 3)}{" "}
                            / {c.active}
                            {c.score !== undefined && (
                              <Typography variant="caption" display="block">
                                RTT {metric(c.effective_rtt_ms ?? null)} ms ·
                                failures {c.consecutive_failures ?? 0} · error
                                EWMA {c.error_ewma ?? 0}
                                <br />
                                {c.formula}
                                <br />
                                {c.speed_used_bps
                                  ? `${metric((c.speed_used_bps * 8) / 1e6)} Mbps · ${c.speed_source}`
                                  : "Скорость в оценке не использована"}
                                <br />
                                Проба:{" "}
                                {c.measurement_at
                                  ? new Date(
                                      c.measurement_at * 1000,
                                    ).toLocaleTimeString()
                                  : "нет данных"}
                              </Typography>
                            )}
                          </TableCell>
                          <TableCell>
                            {reason[c.reason] || c.reason}
                            {c.factors?.map((f) => (
                              <Typography
                                key={f}
                                variant="caption"
                                display="block"
                              >
                                {reason[f] || f}
                              </Typography>
                            ))}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                )}
                {decision.truncated && !decision.observation && (
                  <Alert severity="warning">Список альтернатив усечён.</Alert>
                )}
              </>
            )}
            <Alert severity="info">
              Просмотр read-only. Настроенное, применённое и наблюдаемое
              различаются; текущий каталог открывается на существующих
              страницах. Карта не меняет политику.
            </Alert>
          </Stack>
        </Drawer>
      </Stack>
    </ThemeProvider>
  );
}
