import { useState, useEffect, useMemo, useDeferredValue } from "react";
import {
  Alert,
  Autocomplete,
  Box,
  Button,
  Checkbox,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Divider,
  FormControlLabel,
  IconButton,
  MenuItem,
  Stack,
  Switch,
  Tab,
  Tabs,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import AddRounded from "@mui/icons-material/AddRounded";
import CloseRounded from "@mui/icons-material/CloseRounded";
import DeleteOutlineRounded from "@mui/icons-material/DeleteOutlineRounded";
import InfoOutlined from "@mui/icons-material/InfoOutlined";
import { type GridColDef, type GridRowSelectionModel } from "@mui/x-data-grid";
import { api, message, type Item } from "./api";
import { DataGrid } from "./data-grid";
import { countRu } from "./counts";
import { SourceURLField } from "./source-url-field";

export const labels: Record<string, string> = {
  sources: "Подписки",
  aggregates: "Агрегаты",
  pools: "Пулы",
  profiles: "Профили",
  policies: "Политики",
  listeners: "Точки входа",
};
export const modeLabels: Record<string, string> = {
  auto: "Автоматический",
  throughput: "Максимальная скорость",
  latency: "Минимальная задержка",
  sticky: "Закрепление маршрута",
  fallback: "Основной и резервные",
  "round-robin": "По очереди",
  timed: "Переключение по таймеру",
  manual: "Первый выбранный профиль",
};
const modeHelp: Record<string, string> = {
  auto: "Задержка, нагрузка и свежие активные замеры. Проверяет новые профили, отводит неисправные в карантин.",
  throughput:
    "Свежие замеры скорости имеют приоритет. Задайте URL тестового файла и бюджет.",
  latency: "Предпочитает низкую задержку с поправкой на текущую нагрузку.",
  sticky:
    "Новые соединения закрепляются за одним здоровым маршрутом на заданное время.",
  fallback: "Использует первый доступный профиль в порядке пула.",
  "round-robin": "Распределяет новые соединения по готовым профилям.",
  timed:
    "Меняет путь для новых соединений по таймеру. Открытые сеансы сохраняются.",
  manual:
    "Использует только первый профиль пула. При его отказе новые соединения отклоняются.",
};
const defaults: Record<string, any> = {
  sources: {
    url: "",
    headers: {},
    enabled: true,
    interval_seconds: 3600,
    max_stale_seconds: 259200,
    allow_private: false,
    core_version: "26.10.17",
    filter: {},
  },
  aggregates: { source_ids: [], prefix_names: true, dedupe: true },
  pools: {
    aggregate_id: "",
    source_ids: [],
    profile_ids: [],
    filter: { excluded_ids: [] },
    enabled: true,
  },
  policies: {
    mode: "auto",
    pool_id: "",
    active_paths: 4,
    warm_reserve: 2,
    sticky_seconds: 600,
    rotation_seconds: 30,
    probe_interval_seconds: 30,
    probe_url: "https://www.gstatic.com/generate_204",
    probe_scope: "auto",
    probe_fallback_urls: ["https://cp.cloudflare.com/generate_204"],
    speed_url: "",
    probe_budget_bytes: 134217728,
    max_connections: 5000,
    connect_timeout_ms: 3000,
    fail_closed: true,
  },
  listeners: {
    protocol: "socks5",
    listen: "0.0.0.0:11080",
    policy_id: "",
    username: "",
    password: "",
    udp: true,
    enabled: true,
    allow_cidrs: [],
  },
  profiles: { enabled: true, core_version: "" },
};
const fields: Record<string, string[]> = {
  sources: [
    "url",
    "headers",
    "enabled",
    "interval_seconds",
    "max_stale_seconds",
    "allow_private",
    "core_version",
    "filter",
  ],
  aggregates: ["source_ids", "prefix_names", "dedupe"],
  pools: ["aggregate_id", "source_ids", "profile_ids", "filter", "enabled"],
  profiles: ["enabled", "core_version"],
  policies: Object.keys(defaults.policies),
  listeners: Object.keys(defaults.listeners),
};
export function FieldHelp({ children }: { children: string }) {
  return (
    <Tooltip title={children} arrow>
      <InfoOutlined
        sx={{
          fontSize: 17,
          color: "text.secondary",
          verticalAlign: "middle",
          ml: 0.5,
        }}
      />
    </Tooltip>
  );
}
export function FilterEditor({
  value,
  onChange,
}: {
  value: any;
  onChange: (v: any) => void;
}) {
  const v = value || {};
  const patch = (key: string, data: any) => onChange({ ...v, [key]: data });
  const conditions = v.conditions || [];
  return (
    <Stack spacing={2}>
      <Typography variant="subtitle2">
        Фильтр профилей
        <FieldHelp>
          Фильтры применяются к нормализованным данным. Страна определяется по
          имени и может быть неизвестна.
        </FieldHelp>
      </Typography>
      <Box className="two-col">
        <Autocomplete
          multiple
          freeSolo
          options={["DE", "NL", "FR", "FI", "US", "RU", "GB", "SE", "PL"]}
          value={v.countries || []}
          onChange={(_, x) =>
            patch(
              "countries",
              x.map((s) => s.toUpperCase()),
            )
          }
          renderInput={(p) => (
            <TextField {...p} label="Только страны" placeholder="Все страны" />
          )}
        />
        <Autocomplete
          multiple
          options={["vless", "vmess", "trojan", "shadowsocks", "socks", "http"]}
          value={v.protocols || []}
          onChange={(_, x) => patch("protocols", x)}
          renderInput={(p) => (
            <TextField {...p} label="Протоколы" placeholder="Все протоколы" />
          )}
        />
      </Box>
      <FormControlLabel
        control={
          <Switch
            checked={!!v.udp_only}
            onChange={(_, x) => patch("udp_only", x)}
          />
        }
        label="Только профили с UDP"
      />
      {conditions.length > 0 && (
        <TextField
          select
          label="Объединение условий"
          value={v.mode || "all"}
          onChange={(e) => patch("mode", e.target.value)}
        >
          <MenuItem value="all">Все условия</MenuItem>
          <MenuItem value="any">Любое условие</MenuItem>
        </TextField>
      )}
      {conditions.map((c: any, i: number) => (
        <Box
          key={i}
          sx={{
            display: "flex",
            gap: 1,
            alignItems: "start",
            flexWrap: { xs: "wrap", sm: "nowrap" },
          }}
        >
          <TextField
            select
            label="Поле"
            value={c.field}
            sx={{ minWidth: 120 }}
            onChange={(e) =>
              patch(
                "conditions",
                conditions.map((x: any, n: number) =>
                  n === i ? { ...x, field: e.target.value } : x,
                ),
              )
            }
          >
            {Object.entries({
              name: "Название",
              host: "Сервер",
              protocol: "Протокол",
              country: "Страна",
              transport: "Транспорт",
            }).map(([id, label]) => (
              <MenuItem key={id} value={id}>
                {label}
              </MenuItem>
            ))}
          </TextField>
          <TextField
            select
            label="Условие"
            value={c.operator}
            sx={{ minWidth: 155 }}
            onChange={(e) =>
              patch(
                "conditions",
                conditions.map((x: any, n: number) =>
                  n === i ? { ...x, operator: e.target.value } : x,
                ),
              )
            }
          >
            {Object.entries({
              contains: "Содержит",
              not_contains: "Не содержит",
              equals: "Равно",
              not_equals: "Не равно",
              starts_with: "Начинается с",
              regex: "Регулярное (RE2)",
            }).map(([id, label]) => (
              <MenuItem key={id} value={id}>
                {label}
              </MenuItem>
            ))}
          </TextField>
          <TextField
            label="Значение"
            value={c.value}
            fullWidth
            onChange={(e) =>
              patch(
                "conditions",
                conditions.map((x: any, n: number) =>
                  n === i ? { ...x, value: e.target.value } : x,
                ),
              )
            }
            helperText={
              c.operator === "regex"
                ? "RE2: без lookahead и lookbehind"
                : undefined
            }
          />
          <IconButton
            aria-label="Удалить условие"
            onClick={() =>
              patch(
                "conditions",
                conditions.filter((_: any, n: number) => n !== i),
              )
            }
          >
            <CloseRounded />
          </IconButton>
        </Box>
      ))}
      <Button
        startIcon={<AddRounded />}
        onClick={() =>
          patch("conditions", [
            ...conditions,
            { field: "name", operator: "contains", value: "" },
          ])
        }
        sx={{ alignSelf: "start" }}
      >
        Добавить условие
      </Button>
    </Stack>
  );
}
export function SourcePicker({
  sources,
  value,
  onChange,
}: {
  sources: Item[];
  value: string[];
  onChange: (v: string[]) => void;
}) {
  return (
    <Box
      sx={{
        maxHeight: 290,
        overflow: "auto",
        border: 1,
        borderColor: "divider",
        borderRadius: 3,
        p: 1,
      }}
    >
      {sources.length === 0 ? (
        <Typography color="text.secondary" p={2}>
          Сначала добавьте подписку.
        </Typography>
      ) : (
        sources.map((s) => (
          <FormControlLabel
            key={s.id}
            sx={{
              display: "flex",
              m: 0,
              p: 0.5,
              borderRadius: 2,
              "&:hover": { bgcolor: "action.hover" },
            }}
            control={
              <Checkbox
                checked={value.includes(s.id)}
                onChange={(_, checked) =>
                  onChange(
                    checked
                      ? [...value, s.id]
                      : value.filter((id) => id !== s.id),
                  )
                }
              />
            }
            label={
              <Box>
                <Typography variant="body2" fontWeight={600}>
                  {s.name}
                </Typography>
                <Typography variant="caption" color="text.secondary">
                  {countRu(
                    s.data.profile_count || 0,
                    "профиль",
                    "профиля",
                    "профилей",
                  )}{" "}
                  · {s.data.enabled ? "включена" : "выключена"}
                </Typography>
              </Box>
            }
          />
        ))
      )}
    </Box>
  );
}
export function Editor({
  kind,
  item,
  catalog,
  onClose,
  onSaved,
}: {
  kind: string;
  item?: Item;
  catalog: Record<string, Item[]>;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [name, setName] = useState(item?.name || "");
  const [data, setData] = useState<any>(() => ({
    ...defaults[kind],
    ...item?.data,
    ...(kind === "profiles"
      ? {
          core_version: item?.data.core_override ? item?.data.core_version : "",
        }
      : {}),
  }));
  const [listenerBind, setListenerBind] = useState("0.0.0.0");
  const listenerAddress = (port: number, bind = listenerBind) =>
    `${bind.includes(":") ? `[${bind}]` : bind}:${port}`;
  const [listenerPortsLoaded, setListenerPortsLoaded] = useState(
    kind !== "listeners" || !!item,
  );
  const [listenerPorts, setListenerPorts] = useState<Record<string, number>>({
    http: 18080,
    socks5: 11080,
  });
  useEffect(() => {
    if (kind !== "listeners" || item) return;
    let live = true;
    api("client-access")
      .then((value) => {
        if (!live) return;
        setListenerPorts(value.default_ports);
        setListenerBind(value.default_bind);
        setListenerPortsLoaded(true);
        setData((old: any) => ({
          ...old,
          listen: ["0.0.0.0:11080", "0.0.0.0:18080"].includes(old.listen)
            ? listenerAddress(
                value.default_ports[old.protocol],
                value.default_bind,
              )
            : old.listen,
        }));
      })
      .catch((e) => {
        if (live) setError(message(e));
      });
    return () => {
      live = false;
    };
  }, [kind, item]);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [tab, setTab] = useState(0);
  const [manual, setManual] = useState(
    (item?.data.profile_ids?.length || 0) > 0,
  );
  const [search, setSearch] = useState("");
  const set = (key: string, value: any) =>
    setData((v: any) => ({ ...v, [key]: value }));
  const input = (
    key: string,
    label: string,
    extra: Record<string, any> = {},
  ) => (
    <TextField
      label={label}
      value={data[key] ?? ""}
      onChange={(e) =>
        set(
          key,
          extra.type === "number" ? Number(e.target.value) : e.target.value,
        )
      }
      fullWidth
      {...extra}
    />
  );
  const toggle = (key: string, label: string) => (
    <FormControlLabel
      control={
        <Switch checked={!!data[key]} onChange={(_, v) => set(key, v)} />
      }
      label={label}
    />
  );
  const select = (key: string, label: string, items: Item[], empty = false) => (
    <TextField
      required={!empty}
      select
      label={label}
      value={data[key] || ""}
      onChange={(e) => set(key, e.target.value)}
      fullWidth
    >
      {empty && <MenuItem value="">Не выбран</MenuItem>}
      {items.map((v) => (
        <MenuItem key={v.id} value={v.id}>
          {v.name}
        </MenuItem>
      ))}
    </TextField>
  );
  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setError("");
    setSaving(true);
    try {
      const outgoing: Record<string, any> = {};
      for (const key of fields[kind])
        if (data[key] !== undefined) outgoing[key] = data[key];
      if (kind === "pools" && !manual) outgoing.profile_ids = [];
      await api(
        kind + (item ? "/" + item.id : ""),
        item ? "PATCH" : "POST",
        { name, data: outgoing },
        item?.version,
      );
      onSaved();
    } catch (e) {
      setError(message(e));
    } finally {
      setSaving(false);
    }
  };
  const sourceIds = useMemo(
    () =>
      new Set<string>([
        ...(data.source_ids || []),
        ...((catalog.aggregates || []).find((a) => a.id === data.aggregate_id)
          ?.data.source_ids || []),
      ]),
    [data.source_ids, data.aggregate_id, catalog.aggregates],
  );
  const candidates = useMemo(
    () =>
      (catalog.profiles || []).filter(
        (p) => manual || (sourceIds.has(p.data.source_id) && p.data.present),
      ),
    [catalog.profiles, manual, sourceIds],
  );
  const candidateIds = useMemo(
    () => new Set(candidates.map((p) => p.id)),
    [candidates],
  );
  const deferredSearch = useDeferredValue(search.toLowerCase());
  const profileRows = useMemo(
    () =>
      candidates.filter(
        (p) =>
          !deferredSearch ||
          `${p.name} ${p.data.protocol} ${p.data.host} ${p.data.country}`
            .toLowerCase()
            .includes(deferredSearch),
      ),
    [candidates, deferredSearch],
  );
  const exclusions = useMemo(
    () => new Set<string>(data.filter?.excluded_ids || []),
    [data.filter?.excluded_ids],
  );
  const selected = useMemo(
    () =>
      manual
        ? new Set<string>(data.profile_ids || [])
        : new Set<string>(
            candidates.filter((p) => !exclusions.has(p.id)).map((p) => p.id),
          ),
    [manual, data.profile_ids, candidates, exclusions],
  );
  const columns: GridColDef[] = [
    { field: "name", headerName: "Профиль", flex: 1, minWidth: 180 },
    {
      field: "protocol",
      headerName: "Протокол",
      width: 105,
      valueGetter: (_, row) => row.data.protocol,
    },
    {
      field: "country",
      headerName: "Страна",
      width: 90,
      valueGetter: (_, row) => row.data.country || "?",
    },
    {
      field: "udp",
      headerName: "UDP",
      width: 70,
      valueGetter: (_, row) => (row.data.udp ? "Да" : "Нет"),
    },
    {
      field: "state",
      headerName: "Поддержка",
      width: 120,
      valueGetter: (_, row) =>
        row.data.supported ? "Доступен" : "Не поддержан",
    },
  ];
  return (
    <Dialog
      open
      onClose={saving ? undefined : onClose}
      maxWidth="md"
      fullWidth
      scroll="paper"
      slotProps={{ paper: { component: "form", onSubmit: save } }}
    >
      <DialogTitle
        sx={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
        }}
      >
        {item ? "Редактировать" : "Добавить"} · {labels[kind]}
        <IconButton
          aria-label="Закрыть редактор"
          onClick={onClose}
          disabled={saving || !listenerPortsLoaded}
        >
          <CloseRounded />
        </IconButton>
      </DialogTitle>
      <DialogContent dividers>
        <Stack spacing={3} pt={1}>
          {error && (
            <Alert severity="error" role="alert">
              {error}
            </Alert>
          )}
          {kind !== "profiles" && (
            <TextField
              required
              autoFocus
              label="Название"
              value={name}
              onChange={(e) => setName(e.target.value)}
              fullWidth
              slotProps={{ htmlInput: { maxLength: 160 } }}
            />
          )}
          {kind === "sources" && (
            <>
              <Tabs value={tab} onChange={(_, v) => setTab(v)}>
                <Tab label="Источник" />
                <Tab label="Фильтры" />
              </Tabs>
              {tab === 0 ? (
                <Stack spacing={2.5}>
                  <SourceURLField
                    sourceId={item?.id}
                    sourceVersion={item?.version}
                    value={data.url || ""}
                    onChange={(value) => set("url", value)}
                    required={!item}
                    hint={item?.data.url_hint}
                    configured={!!item?.data.url_configured}
                    helperText={
                      item
                        ? "Оставьте пустым, чтобы сохранить действующий URL. Секретная часть скрыта."
                        : "URI-список, base64, Xray JSON, sing-box JSON или Mihomo YAML"
                    }
                  />
                  <Box className="two-col">
                    {
                      <TextField
                        type="number"
                        label="Обновлять, мин"
                        value={data.interval_seconds / 60}
                        onChange={(e) =>
                          set("interval_seconds", Number(e.target.value) * 60)
                        }
                        helperText="0 - только вручную"
                        slotProps={{ htmlInput: { min: 0, max: 1440 } }}
                      />
                    }
                    <TextField
                      type="number"
                      label="Допустимый возраст, ч"
                      value={data.max_stale_seconds / 3600}
                      onChange={(e) =>
                        set("max_stale_seconds", Number(e.target.value) * 3600)
                      }
                      slotProps={{ htmlInput: { min: 1, max: 720 } }}
                    />
                  </Box>
                  {input("core_version", "Версия Xray", {
                    helperText:
                      "Например, 26.10.17. Сборка должна быть одобрена в разделе «Ядра».",
                  })}
                  {toggle("enabled", "Подписка включена")}
                  {toggle(
                    "allow_private",
                    "Разрешить частные адреса источника",
                  )}
                  <Typography variant="subtitle2">
                    Заголовки запроса
                    <FieldHelp>
                      Например, User-Agent или заголовок HWID. Значения хранятся
                      зашифрованными.
                    </FieldHelp>
                  </Typography>
                  {Object.entries(data.headers || {}).map(([key, value], i) => (
                    <Box key={i} sx={{ display: "flex", gap: 1 }}>
                      <TextField
                        label="Заголовок"
                        value={key}
                        onChange={(e) => {
                          const entries = Object.entries(data.headers);
                          entries[i] = [e.target.value, value];
                          set("headers", Object.fromEntries(entries));
                        }}
                      />
                      <TextField
                        label="Значение"
                        type="password"
                        value={String(value)}
                        fullWidth
                        onChange={(e) =>
                          set("headers", {
                            ...data.headers,
                            [key]: e.target.value,
                          })
                        }
                      />
                      <IconButton
                        aria-label="Удалить заголовок"
                        onClick={() => {
                          const next = { ...data.headers };
                          delete next[key];
                          set("headers", next);
                        }}
                      >
                        <DeleteOutlineRounded />
                      </IconButton>
                    </Box>
                  ))}
                  <Button
                    startIcon={<AddRounded />}
                    sx={{ alignSelf: "start" }}
                    onClick={() =>
                      set("headers", {
                        ...data.headers,
                        ["X-Header-" +
                        (Object.keys(data.headers || {}).length + 1)]: "",
                      })
                    }
                  >
                    Добавить заголовок
                  </Button>
                </Stack>
              ) : (
                <FilterEditor
                  value={data.filter}
                  onChange={(v) => set("filter", v)}
                />
              )}
            </>
          )}
          {kind === "aggregates" && (
            <>
              <Typography variant="subtitle2">
                Подписки в порядке приоритета
              </Typography>
              <SourcePicker
                sources={catalog.sources || []}
                value={data.source_ids || []}
                onChange={(v) => set("source_ids", v)}
              />
              <Typography variant="caption" color="text.secondary">
                Порядок выбора задаёт порядок источников. Снимите отметку и
                выберите снова, чтобы перенести подписку в конец.
              </Typography>
              {toggle(
                "prefix_names",
                "Добавлять имя подписки к названию профиля",
              )}
              {toggle("dedupe", "Удалять точные дубли с учётом учётных данных")}
            </>
          )}
          {kind === "pools" && (
            <>
              <Tabs
                value={tab}
                onChange={(_, v) => setTab(v)}
                variant="scrollable"
              >
                <Tab label="Источники" />
                <Tab label="Фильтры" />
                <Tab label="Выбор профилей" />
              </Tabs>
              {tab === 0 && (
                <Stack spacing={2}>
                  {select(
                    "aggregate_id",
                    "Агрегат",
                    catalog.aggregates || [],
                    true,
                  )}
                  <Typography variant="subtitle2">
                    Дополнительные подписки
                  </Typography>
                  <SourcePicker
                    sources={catalog.sources || []}
                    value={data.source_ids || []}
                    onChange={(v) => set("source_ids", v)}
                  />
                  {toggle("enabled", "Пул включён")}
                </Stack>
              )}
              {tab === 1 && (
                <FilterEditor
                  value={data.filter}
                  onChange={(v) => set("filter", v)}
                />
              )}
              {tab === 2 && (
                <Stack spacing={2}>
                  <FormControlLabel
                    control={
                      <Switch
                        checked={manual}
                        onChange={(_, v) => {
                          setManual(v);
                          set("profile_ids", v ? Array.from(selected) : []);
                        }}
                      />
                    }
                    label="Ручной список вместо всех профилей источников"
                  />
                  <Alert severity="info">
                    {manual
                      ? "Новые профили не добавляются автоматически."
                      : "Все профили выбранных источников, кроме снятых отметок. Новые профили будут добавляться автоматически."}{" "}
                    Итоговый список также учитывает фильтры, срок годности,
                    квоты и поддержку протокола.
                  </Alert>
                  <TextField
                    label="Найти профиль"
                    value={search}
                    onChange={(e) => setSearch(e.target.value)}
                  />
                  <Box sx={{ height: 400 }}>
                    <DataGrid
                      rows={profileRows}
                      columns={columns}
                      checkboxSelection
                      disableRowSelectionOnClick
                      disableRowSelectionExcludeModel
                      keepNonExistentRowsSelected
                      rowSelectionModel={{ type: "include", ids: selected }}
                      onRowSelectionModelChange={(
                        model: GridRowSelectionModel,
                      ) => {
                        const ids = Array.from(model.ids).map(String);
                        if (manual) set("profile_ids", ids);
                        else
                          set("filter", {
                            ...data.filter,
                            excluded_ids: [
                              ...(data.filter?.excluded_ids || []).filter(
                                (id: string) => !candidateIds.has(id),
                              ),
                              ...candidates
                                .filter((p) => !model.ids.has(p.id))
                                .map((p) => p.id),
                            ],
                          });
                      }}
                      pageSizeOptions={[25, 50, 100]}
                      initialState={{
                        pagination: { paginationModel: { pageSize: 25 } },
                      }}
                    />
                  </Box>
                  <Typography variant="caption">
                    Отмечено: {selected.size}. Каталог:{" "}
                    {(catalog.profiles || []).length}.
                  </Typography>
                </Stack>
              )}
            </>
          )}
          {kind === "policies" && (
            <>
              {select("pool_id", "Пул профилей", catalog.pools || [])}
              <TextField
                select
                label="Режим распределения"
                value={data.mode}
                onChange={(e) => set("mode", e.target.value)}
                helperText={modeHelp[data.mode]}
              >
                {Object.entries(modeLabels).map(([id, label]) => (
                  <MenuItem key={id} value={id}>
                    {label}
                  </MenuItem>
                ))}
              </TextField>
              <Box className="two-col">
                {input("active_paths", "Активных путей", {
                  type: "number",
                  slotProps: { htmlInput: { min: 1, max: 16 } },
                })}
                {input("warm_reserve", "Готовых резервных", {
                  type: "number",
                  slotProps: { htmlInput: { min: 0, max: 8 } },
                })}
                {input("max_connections", "Лимит соединений", {
                  type: "number",
                  slotProps: { htmlInput: { min: 1, max: 5000 } },
                })}
                {input("connect_timeout_ms", "Тайм-аут подключения, мс", {
                  type: "number",
                })}
                {input("sticky_seconds", "Закрепление, с", { type: "number" })}
                {input("rotation_seconds", "Интервал переключения, с", {
                  type: "number",
                })}
              </Box>
              <Divider />
              <Typography variant="subtitle2">Проверки и бюджет</Typography>
              <TextField
                select
                label="Назначение проверки"
                value={data.probe_scope || "auto"}
                onChange={(e) => set("probe_scope", e.target.value)}
                helperText="HTML-сайт с защитой от ботов может отклонять проверку, хотя загрузки через прокси работают."
              >
                <MenuItem value="auto">Автоматически</MenuItem>
                <MenuItem value="shared">Общая доступность ядра</MenuItem>
                <MenuItem value="target">
                  Обязательная доступность цели
                </MenuItem>
              </TextField>
              {input("probe_url", "URL проверки доступности", {
                type: "url",
                required: true,
              })}
              <Autocomplete
                multiple
                freeSolo
                options={[]}
                value={data.probe_fallback_urls || []}
                onChange={(_, v) => set("probe_fallback_urls", v)}
                renderInput={(p) => (
                  <TextField
                    {...p}
                    label="Резервные URL проверки"
                    helperText="До двух адресов. Отказ одного тестового сервиса не исключит все маршруты."
                  />
                )}
              />
              {input("probe_interval_seconds", "Интервал проверки, с", {
                type: "number",
                slotProps: { htmlInput: { min: 10, max: 3600 } },
              })}
              {input("speed_url", "URL тестового файла", {
                type: "url",
                helperText:
                  "Необязательно. Без URL активные тесты скорости отключены.",
              })}
              <TextField
                type="number"
                label="Бюджет тестов скорости, МиБ в сутки"
                value={data.probe_budget_bytes / 1048576}
                onChange={(e) =>
                  set("probe_budget_bytes", Number(e.target.value) * 1048576)
                }
              />
              <Alert severity="info">
                При отсутствии здоровых маршрутов новые соединения отклоняются.
                Прямого обхода прокси нет. Один поток не объединяет пропускную
                способность нескольких серверов.
              </Alert>
            </>
          )}
          {kind === "listeners" && (
            <>
              <Box className="two-col">
                <TextField
                  select
                  label="Протокол"
                  disabled={!listenerPortsLoaded}
                  value={data.protocol}
                  onChange={(e) => {
                    if (
                      !item &&
                      data.listen ===
                        listenerAddress(listenerPorts[data.protocol])
                    )
                      set(
                        "listen",
                        listenerAddress(listenerPorts[e.target.value]),
                      );
                    set("protocol", e.target.value);
                    if (e.target.value === "http") set("udp", false);
                  }}
                >
                  <MenuItem value="socks5">SOCKS5</MenuItem>
                  <MenuItem value="http">HTTP / CONNECT</MenuItem>
                </TextField>
                {input("listen", "Адрес bind", {
                  disabled: !listenerPortsLoaded,
                  required: true,
                  helperText: `Порт внутри и снаружи совпадает. HTTP по умолчанию ${listenerPorts.http}, SOCKS5 ${listenerPorts.socks5}. Другие порты требуют deployment-настройки.`,
                })}
              </Box>
              {select("policy_id", "Политика", catalog.policies || [])}
              {toggle("enabled", "Точка входа включена")}
              {data.protocol === "socks5" &&
                toggle("udp", "Разрешить UDP ASSOCIATE")}
              <Divider />
              <Typography variant="subtitle2">
                Аутентификация клиентов
              </Typography>
              <Box className="two-col">
                {input("username", "Имя пользователя", {
                  autoComplete: "off",
                  helperText: "Пустые имя и пароль - без аутентификации.",
                })}
                {input("password", "Пароль", {
                  type: "password",
                  autoComplete: "new-password",
                  helperText: item?.data.password_configured
                    ? "Пустое поле сохраняет текущий пароль."
                    : undefined,
                })}
              </Box>
              <TextField
                label="Разрешённые сети (CIDR)"
                value={(data.allow_cidrs || []).join(", ")}
                onChange={(e) =>
                  set(
                    "allow_cidrs",
                    e.target.value
                      .split(",")
                      .map((v) => v.trim())
                      .filter(Boolean),
                  )
                }
                helperText="Пусто - все клиенты, которым доступен опубликованный порт. Доступ снаружи ограничьте LAN/NetBird."
              />
              <Alert severity="info">
                Для UDP укажите адрес сервера и диапазон опубликованных
                UDP-портов в настройках. Клиент должен поддерживать SOCKS5 UDP
                ASSOCIATE.
              </Alert>
            </>
          )}
          {kind === "profiles" && (
            <>
              <Typography variant="h6">{item?.name}</Typography>
              <Chip
                label={`${item?.data.protocol} · ${item?.data.host}:${item?.data.port}`}
                sx={{ alignSelf: "start" }}
              />
              {toggle("enabled", "Профиль включён")}
              {input("core_version", "Версия Xray для этого профиля", {
                helperText:
                  "Пусто - наследовать версию подписки/системы. Можно выбрать отдельную версию.",
              })}
            </>
          )}
        </Stack>
      </DialogContent>
      <DialogActions sx={{ p: 2.5 }}>
        <Button onClick={onClose} disabled={saving || !listenerPortsLoaded}>
          Отмена
        </Button>
        <Button variant="contained" type="submit" loading={saving}>
          Сохранить
        </Button>
      </DialogActions>
    </Dialog>
  );
}
