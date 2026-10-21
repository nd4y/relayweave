import { ClientAccess } from "./client-access";
import type {} from "@mui/x-data-grid/themeAugmentation";
import { ruRU as gridRu } from "@mui/x-data-grid/locales";
import React, {
  useCallback,
  useEffect,
  useMemo,
  useState,
  useRef,
  useDeferredValue,
} from "react";
import { createRoot } from "react-dom/client";
import {
  Alert,
  AppBar,
  Avatar,
  Box,
  Button,
  Card,
  CardActions,
  CardContent,
  Chip,
  CircularProgress,
  CssBaseline,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Divider,
  Drawer,
  Fade,
  FormControlLabel,
  IconButton,
  InputAdornment,
  LinearProgress,
  List,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  Paper,
  Snackbar,
  Stack,
  Switch,
  Tab,
  Tabs,
  TextField,
  ThemeProvider,
  Toolbar,
  Tooltip,
  Typography,
  createTheme,
} from "@mui/material";
import AddRounded from "@mui/icons-material/AddRounded";
import AltRouteRounded from "@mui/icons-material/AltRouteRounded";
import ArrowForwardRounded from "@mui/icons-material/ArrowForwardRounded";
import AutoAwesomeRounded from "@mui/icons-material/AutoAwesomeRounded";
import BarChartRounded from "@mui/icons-material/BarChartRounded";
import BoltRounded from "@mui/icons-material/BoltRounded";
import CheckCircleOutlineRounded from "@mui/icons-material/CheckCircleOutlineRounded";
import CloseRounded from "@mui/icons-material/CloseRounded";
import CloudDownloadRounded from "@mui/icons-material/CloudDownloadRounded";
import DarkModeRounded from "@mui/icons-material/DarkModeRounded";
import DashboardRounded from "@mui/icons-material/DashboardRounded";
import DeleteOutlineRounded from "@mui/icons-material/DeleteOutlineRounded";
import DescriptionRounded from "@mui/icons-material/DescriptionRounded";
import EditRounded from "@mui/icons-material/EditRounded";
import FilterAltRounded from "@mui/icons-material/FilterAltRounded";
import HubRounded from "@mui/icons-material/HubRounded";
import InfoOutlined from "@mui/icons-material/InfoOutlined";
import LightModeRounded from "@mui/icons-material/LightModeRounded";
import LinkRounded from "@mui/icons-material/LinkRounded";
import LogoutRounded from "@mui/icons-material/LogoutRounded";
import MenuRounded from "@mui/icons-material/MenuRounded";
import PauseRounded from "@mui/icons-material/PauseRounded";
import PlayArrowRounded from "@mui/icons-material/PlayArrowRounded";
import RefreshRounded from "@mui/icons-material/RefreshRounded";
import RestartAltRounded from "@mui/icons-material/RestartAltRounded";
import SearchRounded from "@mui/icons-material/SearchRounded";
import SettingsRounded from "@mui/icons-material/SettingsRounded";
import ShieldRounded from "@mui/icons-material/ShieldRounded";
import StorageRounded from "@mui/icons-material/StorageRounded";
import SwapHorizRounded from "@mui/icons-material/SwapHorizRounded";
import TerminalRounded from "@mui/icons-material/TerminalRounded";
import VisibilityOffRounded from "@mui/icons-material/VisibilityOffRounded";
import VisibilityRounded from "@mui/icons-material/VisibilityRounded";
import WarningAmberRounded from "@mui/icons-material/WarningAmberRounded";
import { type GridColDef } from "@mui/x-data-grid";
import { DataGrid } from "./data-grid";
import { ago, all, api, downloadBackup, message, type Item } from "./api";
import { Editor, labels, modeLabels } from "./editor";
import { CoreApproval } from "./core-approval";
import { CoreCache } from "./core-cache";
import { countRu } from "./counts";
import { TrafficTopology } from "./topology";
import "./styles.css";

const grafanaURL = import.meta.env.VITE_GRAFANA_URL?.trim() || "";
const pages = [
  ["overview", "Обзор", DashboardRounded],
  ["topology", "Карта трафика", HubRounded],
  ["sources", "Подписки", LinkRounded],
  ["aggregates", "Агрегаты", HubRounded],
  ["pools", "Пулы профилей", FilterAltRounded],
  ["profiles", "Все профили", AltRouteRounded],
  ["policies", "Политики", AutoAwesomeRounded],
  ["listeners", "Точки входа", SwapHorizRounded],
  ["connections", "Подключение", SwapHorizRounded],
  ["workers", "Воркеры", StorageRounded],
  ["cores", "Ядра", BoltRounded],
  ["events", "Журнал", TerminalRounded],
  ["settings", "Настройки", SettingsRounded],
  ["help", "Помощь и API", DescriptionRounded],
] as const;
const subtitles: Record<string, string> = {
  connections: "Конфигурация HTTP/SOCKS5 для Throne и других клиентов",
  topology: "Наблюдаемые пути, серверные IP и фактические решения",
  overview: "Состояние сети, маршрутов и подписок в одном месте",
  sources: "Объединяйте подписки, сохраняя контроль над каждым источником",
  aggregates: "Соберите несколько подписок в один упорядоченный каталог",
  pools: "Выберите профили и правила включения для маршрутизации",
  profiles: "Общий каталог с поддержкой протоколов и отдельными версиями ядра",
  policies: "Настройте распределение новых соединений и проверки маршрутов",
  listeners: "HTTP и SOCKS5 для устройств и контейнеров",
  workers: "Состояние процессов, локальных маршрутов и автономной работы",
  cores: "Проверенные версии Xray и установка по запросу",
  events: "История изменений и результаты фоновых операций",
  settings: "Сеть, автономная работа и зашифрованные резервные копии",
  help: "Подключение клиентов, ограничения протоколов и API",
};
type Toast = { text: string; severity: "success" | "error" | "info" };
function Logo({ small = false }: { small?: boolean }) {
  return (
    <Stack direction="row" spacing={1.4} alignItems="center">
      <Box
        component="img"
        src="/relay.svg"
        alt=""
        sx={{ width: small ? 34 : 40, height: small ? 34 : 40 }}
      />
      <Box>
        <Typography
          sx={{
            fontWeight: 800,
            letterSpacing: "-.6px",
            fontSize: small ? 19 : 23,
            lineHeight: 1.2,
          }}
        >
          RelayWeave
        </Typography>
        {!small && (
          <Typography variant="caption" color="text.secondary">
            Your routes. In harmony.
          </Typography>
        )}
      </Box>
    </Stack>
  );
}
function Login({ onLogin }: { onLogin: () => void }) {
  const [password, setPassword] = useState("");
  const [show, setShow] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <Box className="login-layout">
      <Box className="login-orbit orbit-one" />
      <Box className="login-orbit orbit-two" />
      <Fade in timeout={500}>
        <Paper className="login-card" elevation={0}>
          <Logo />
          <Box mt={5} mb={3}>
            <Chip
              label="Личная прокси-платформа"
              size="small"
              color="primary"
              variant="outlined"
            />
            <Typography variant="h4" mt={2} fontWeight={750}>
              Всё под контролем.
            </Typography>
            <Typography color="text.secondary" mt={1}>
              Войдите, чтобы управлять подписками и маршрутами.
            </Typography>
          </Box>
          <Box
            component="form"
            onSubmit={async (e) => {
              e.preventDefault();
              setBusy(true);
              setError("");
              try {
                await api("auth/login", "POST", { password });
                onLogin();
              } catch (e) {
                setError(message(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            <Stack spacing={2.5}>
              {error && <Alert severity="error">{error}</Alert>}
              <TextField
                autoFocus
                required
                fullWidth
                label="Пароль администратора"
                type={show ? "text" : "password"}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="current-password"
                slotProps={{
                  input: {
                    endAdornment: (
                      <InputAdornment position="end">
                        <IconButton
                          aria-label={
                            show ? "Скрыть пароль" : "Показать пароль"
                          }
                          onClick={() => setShow((v) => !v)}
                        >
                          {show ? (
                            <VisibilityOffRounded />
                          ) : (
                            <VisibilityRounded />
                          )}
                        </IconButton>
                      </InputAdornment>
                    ),
                  },
                }}
              />
              <Button
                type="submit"
                size="large"
                variant="contained"
                loading={busy}
                endIcon={<ArrowForwardRounded />}
              >
                Войти
              </Button>
            </Stack>
          </Box>
          <Stack
            direction="row"
            spacing={1}
            mt={4}
            alignItems="center"
            color="text.secondary"
          >
            <ShieldRounded sx={{ fontSize: 16 }} />
            <Typography variant="caption">
              Один администратор · Доступ через LAN / NetBird
            </Typography>
          </Stack>
        </Paper>
      </Fade>
    </Box>
  );
}
function App() {
  const [mode, setMode] = useState<"dark" | "light">(() =>
    localStorage.getItem("theme") === "light" ? "light" : "dark",
  );
  const theme = useMemo(
    () =>
      createTheme({
        palette: {
          mode,
          primary: {
            main: mode === "dark" ? "#c7afff" : "#6941b4",
            contrastText: mode === "dark" ? "#302044" : "#ffffff",
          },
          secondary: { main: mode === "dark" ? "#92ded0" : "#176b60" },
          background: {
            default: mode === "dark" ? "#121016" : "#f7f5fa",
            paper: mode === "dark" ? "#1b1821" : "#ffffff",
          },
          text: {
            primary: mode === "dark" ? "#eee9f5" : "#211a2d",
            secondary: mode === "dark" ? "#aaa3b5" : "#71687d",
          },
          divider: mode === "dark" ? "#36303f" : "#e7e1ed",
          success: { main: mode === "dark" ? "#83d6ab" : "#247a4c" },
        },
        shape: { borderRadius: 16 },
        typography: {
          fontFamily: '"Segoe UI", system-ui, sans-serif',
          h4: { fontSize: "2rem", fontWeight: 750, letterSpacing: "-.8px" },
          h5: { fontWeight: 700 },
          h6: { fontWeight: 650 },
          button: { textTransform: "none", fontWeight: 650 },
        },
        components: {
          MuiButton: {
            defaultProps: { disableElevation: true },
            styleOverrides: {
              root: {
                borderRadius: 14,
                padding: "9px 18px",
                whiteSpace: "nowrap",
                transition:
                  "background-color 180ms, box-shadow 180ms, transform 180ms",
              },
              contained: { "&:active": { transform: "scale(.98)" } },
            },
          },
          MuiCard: {
            defaultProps: { variant: "outlined" },
            styleOverrides: {
              root: { backgroundImage: "none", borderRadius: 24 },
            },
          },
          MuiIconButton: {
            styleOverrides: { root: { minWidth: 44, minHeight: 44 } },
          },
          MuiPaper: { styleOverrides: { root: { backgroundImage: "none" } } },
          MuiTextField: { defaultProps: { size: "small" } },
          MuiOutlinedInput: { styleOverrides: { root: { borderRadius: 12 } } },
          MuiChip: { styleOverrides: { root: { fontWeight: 600 } } },
          MuiDialog: { styleOverrides: { paper: { borderRadius: 24 } } },
          MuiTooltip: { defaultProps: { arrow: true, enterDelay: 400 } },
          MuiDataGrid: {
            defaultProps: {
              localeText: gridRu.components.MuiDataGrid.defaultProps.localeText,
            },
            styleOverrides: { root: { border: 0 } },
          },
        },
      }),
    [mode],
  );
  const [auth, setAuth] = useState<boolean | null>(null);
  const [page, setPage] = useState(() => location.hash.slice(1) || "overview");
  const [mobile, setMobile] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [items, setItems] = useState<Item[]>([]);
  const [overview, setOverview] = useState<any>(null);
  const [catalog, setCatalog] = useState<Record<string, Item[]>>({});
  const [sourceNames, setSourceNames] = useState<Record<string, string>>({});
  const [search, setSearch] = useState("");
  const [editor, setEditor] = useState<{ kind: string; item?: Item } | null>(
    null,
  );
  const [toast, setToast] = useState<Toast | null>(null);
  const [confirm, setConfirm] = useState<{
    title: string;
    text: string;
    action: () => Promise<void>;
  } | null>(null);
  const [preview, setPreview] = useState<any>(null);
  const [previewBusy, setPreviewBusy] = useState(false);
  const previewSequence = useRef(0);
  const closePreview = () => {
    previewSequence.current++;
    setPreviewBusy(false);
    setPreview(null);
  };
  const openPreview = async (path: string, pageNumber = 0, pageSize = 25) => {
    const sequence = ++previewSequence.current;
    const server = path.startsWith("pools/");
    setPreviewBusy(true);
    if (!preview)
      setPreview({
        items: [],
        total: 0,
        ...(server
          ? { _path: path, limit: pageSize, offset: pageNumber * pageSize }
          : {}),
      });
    try {
      const result = await api(
        path +
          (server ? `?limit=${pageSize}&offset=${pageNumber * pageSize}` : ""),
      );
      if (sequence === previewSequence.current)
        setPreview({ ...result, ...(server ? { _path: path } : {}) });
    } catch (e) {
      if (sequence === previewSequence.current) notify(message(e), "error");
    } finally {
      if (sequence === previewSequence.current) setPreviewBusy(false);
    }
  };
  const [plan, setPlan] = useState<any>(null);
  const [operations, setOperations] = useState<Item[]>([]);
  const [activity, setActivity] = useState(false);
  const [other, setOther] = useState<any>({});
  const [dirty, setDirty] = useState(false);
  const notify = (text: string, severity: Toast["severity"] = "success") =>
    setToast({ text, severity });
  const loadSequence = useRef(0);
  const load = useCallback(
    async (quiet = false) => {
      if (!auth) return;
      const sequence = ++loadSequence.current;
      if (!quiet) setLoading(true);
      setError("");
      try {
        const summary = await api("overview");
        if (sequence !== loadSequence.current) return;
        setOverview(summary);
        if (labels[page]) {
          const [data, profileSources] = await Promise.all([
            all(page),
            page === "profiles" ? all("sources") : Promise.resolve(null),
          ]);
          if (sequence !== loadSequence.current) return;
          setItems(data);
          if (profileSources)
            setSourceNames(
              Object.fromEntries(
                profileSources.map((item: Item) => [item.id, item.name]),
              ),
            );
        } else if (["cores", "workers", "events"].includes(page)) {
          const data = await api(page);
          if (sequence !== loadSequence.current) return;
          setOther(data);
        } else if (page === "settings") {
          const data = await all("settings");
          if (sequence !== loadSequence.current) return;
          setItems(data);
          const revisions = await api("config");
          const trash = await api("trash");
          if (sequence !== loadSequence.current) return;
          setOther({ revisions: revisions.items, trash: trash.items });
        }
      } catch (e) {
        if (sequence === loadSequence.current) setError(message(e));
      } finally {
        if (sequence === loadSequence.current) setLoading(false);
      }
    },
    [auth, page],
  );
  useEffect(() => {
    const expired = () => {
      setAuth(false);
      setEditor(null);
      setActivity(false);
      setConfirm(null);
      setPreview(null);
      previewSequence.current++;
      setItems([]);
      setSourceNames({});
      setOverview(null);
      loadSequence.current++;
    };
    window.addEventListener("relayweave-session-expired", expired);
    return () =>
      window.removeEventListener("relayweave-session-expired", expired);
  }, []);
  useEffect(() => {
    api("auth/session")
      .then(() => setAuth(true))
      .catch(() => setAuth(false));
  }, []);
  useEffect(() => {
    load();
    setSearch("");
  }, [load]);
  useEffect(() => {
    const onHash = () => setPage(location.hash.slice(1) || "overview");
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  useEffect(() => {
    if (!auth) return;
    const stream = new EventSource("/api/v1/events/stream");
    let timer: ReturnType<typeof setTimeout>;
    stream.addEventListener("change", () => {
      clearTimeout(timer);
      timer = setTimeout(() => load(true), 400);
    });
    let polling = false,
      disposed = false;
    const poll = setInterval(async () => {
      if (polling) return;
      polling = true;
      try {
        const [summary, ops] = await Promise.all([
          api("overview"),
          api("operations"),
        ]);
        if (!disposed) {
          setOverview(summary);
          setOperations(ops.items);
        }
      } catch {
      } finally {
        polling = false;
      }
    }, 4000);
    return () => {
      disposed = true;
      stream.close();
      clearInterval(poll);
      clearTimeout(timer);
    };
  }, [auth, load]);
  const navigate = (id: string) => {
    location.hash = id;
    setPage(id);
    setMobile(false);
  };
  const openEditor = async (kind: string, item?: Item) => {
    try {
      const dependencies: Record<string, string[]> = {
        sources: [],
        aggregates: ["sources"],
        pools: ["sources", "aggregates", "profiles"],
        profiles: [],
        policies: ["pools"],
        listeners: ["policies"],
      };
      const needed = dependencies[kind] || [];
      const collections = await Promise.all(needed.map(all));
      setCatalog(Object.fromEntries(needed.map((v, i) => [v, collections[i]])));
      setEditor({ kind, item });
    } catch (e) {
      notify(message(e), "error");
    }
  };
  const operate = async (path: string, body: unknown = {}) => {
    try {
      const op = await api<Item>(path, "POST", body);
      setOperations((v) => [op, ...v.filter((x) => x.id !== op.id)]);
      notify("Операция поставлена в очередь", "info");
      setActivity(true);
    } catch (e) {
      notify(message(e), "error");
    }
  };
  const pending = operations.filter((o) =>
    ["queued", "running"].includes(o.data.status),
  );
  const deleteItem = (item: Item) =>
    setConfirm({
      title: "Удалить объект?",
      text: `«${item.name}» будет перемещён в корзину на 24 часа. Если он используется другими настройками, удаление будет отклонено.`,
      action: async () => {
        await api(item.kind + "/" + item.id, "DELETE", undefined, item.version);
        setDirty(true);
        load(true);
        notify("Объект удалён. Восстановление доступно в настройках.");
      },
    });
  const sidebar = (
    <Box
      sx={{ height: "100%", display: "flex", flexDirection: "column", p: 2 }}
    >
      <Box p={1.5} mb={2}>
        <Logo />
      </Box>
      <Typography
        variant="overline"
        color="text.secondary"
        sx={{ px: 2, letterSpacing: 1.6 }}
      >
        Рабочее пространство
      </Typography>
      <List sx={{ flex: 1, pt: 1 }}>
        {pages.map(([id, label, Icon]) => (
          <ListItemButton
            key={id}
            selected={page === id}
            onClick={() => navigate(id)}
            sx={{
              borderRadius: 3,
              my: 0.3,
              py: 1,
              "&.Mui-selected": {
                bgcolor: "primary.main",
                color: "primary.contrastText",
                "& .MuiListItemIcon-root": { color: "inherit" },
                "&:hover": { bgcolor: "primary.main" },
              },
            }}
          >
            <ListItemIcon sx={{ minWidth: 42 }}>
              <Icon sx={{ fontSize: 22 }} />
            </ListItemIcon>
            <ListItemText
              primary={label}
              primaryTypographyProps={{
                fontSize: 14,
                fontWeight: page === id ? 700 : 500,
              }}
            />
          </ListItemButton>
        ))}
      </List>
      <Paper
        variant="outlined"
        sx={{ p: 2, borderRadius: 4, bgcolor: "transparent" }}
      >
        <Stack direction="row" spacing={1} alignItems="center">
          <Box className="status-dot" />
          <Typography variant="caption" fontWeight={650}>
            Личное облако
          </Typography>
        </Stack>
        <Typography variant="caption" color="text.secondary">
          Docker Compose · v0.1
        </Typography>
      </Paper>
    </Box>
  );
  const dataColumns: GridColDef[] = [
    {
      field: "name",
      headerName: "Название",
      minWidth: 240,
      flex: 1,
      renderCell: (p) => (
        <Box
          sx={{
            display: "flex",
            flexDirection: "column",
            justifyContent: "center",
            minWidth: 0,
            width: "100%",
            height: "100%",
          }}
        >
          <Typography noWrap variant="body2" fontWeight={600}>
            {p.row.name}
          </Typography>
          <Typography
            noWrap
            variant="caption"
            color="text.secondary"
            title={sourceNames[p.row.data.source_id] || "Источник не найден"}
            sx={{ width: "100%" }}
          >
            {sourceNames[p.row.data.source_id] || "Источник не найден"}
          </Typography>
        </Box>
      ),
    },
    {
      field: "protocol",
      headerName: "Протокол",
      width: 120,
      valueGetter: (_, r) => r.data.protocol || "-",
    },
    {
      field: "host",
      headerName: "Сервер",
      minWidth: 200,
      flex: 1,
      valueGetter: (_, r) => r.data.host,
    },
    {
      field: "country",
      headerName: "Страна",
      width: 85,
      valueGetter: (_, r) => r.data.country || "?",
    },
    {
      field: "core",
      headerName: "Xray",
      width: 115,
      valueGetter: (_, r) => r.data.core_version || "по умолчанию",
    },
    {
      field: "supported",
      headerName: "Состояние",
      width: 170,
      renderCell: (p) => (
        <Tooltip
          title={
            p.row.data.reason ||
            p.row.data.warnings?.join(", ") ||
            "Поддерживается адаптером Xray"
          }
        >
          <Chip
            size="small"
            label={
              !p.row.data.present
                ? "Удалён в источнике"
                : !p.row.data.supported
                  ? "Не поддержан"
                  : !p.row.data.enabled
                    ? "Выключен"
                    : "Включён"
            }
            color={
              p.row.data.supported && p.row.data.enabled && p.row.data.present
                ? "success"
                : "default"
            }
            variant="outlined"
          />
        </Tooltip>
      ),
    },
    {
      field: "actions",
      headerName: "Действия",
      width: 145,
      sortable: false,
      renderCell: (p) => (
        <Box>
          <Tooltip title="Проверить через прокси">
            <IconButton
              aria-label={"Проверить " + p.row.name}
              onClick={() => operate(`profiles/${p.row.id}/test`)}
            >
              <PlayArrowRounded fontSize="small" />
            </IconButton>
          </Tooltip>
          <Tooltip title="Тест скорости (до 8 МиБ)">
            <IconButton
              aria-label={"Измерить скорость " + p.row.name}
              onClick={() => operate(`profiles/${p.row.id}/speed-test`)}
            >
              <BoltRounded fontSize="small" />
            </IconButton>
          </Tooltip>
          <Tooltip title="Настройки профиля">
            <IconButton
              aria-label={"Редактировать " + p.row.name}
              onClick={() => openEditor("profiles", p.row)}
            >
              <EditRounded fontSize="small" />
            </IconButton>
          </Tooltip>
        </Box>
      ),
    },
  ];
  const deferredSearch = useDeferredValue(search.toLowerCase());
  const searchIndex = useMemo(
    () =>
      items.map((item) => ({
        item,
        text: `${item.name} ${sourceNames[item.data.source_id] || ""} ${item.data.host || ""} ${item.data.protocol || ""} ${item.data.country || ""}`.toLowerCase(),
      })),
    [items, sourceNames],
  );
  const filtered = useMemo(
    () =>
      searchIndex
        .filter((row) => row.text.includes(deferredSearch))
        .map((row) => row.item),
    [searchIndex, deferredSearch],
  );
  const sourceStatus = (v: Item) => {
    const d = v.data;
    if (!d.enabled) return { label: "Выключена", color: "default" as const };
    if (d.quota?.expire > 0 && d.quota.expire * 1000 <= Date.now())
      return { label: "Срок истёк", color: "error" as const };
    if (
      d.quota?.total > 0 &&
      (d.quota.upload || 0) + (d.quota.download || 0) >= d.quota.total
    )
      return { label: "Квота исчерпана", color: "warning" as const };
    if (d.pending_snapshot)
      return { label: "Нужна проверка", color: "warning" as const };
    if (d.last_error)
      return { label: "Ошибка обновления", color: "error" as const };
    if (!d.last_success || d.last_success.startsWith("0001"))
      return { label: "Ожидает обновления", color: "default" as const };
    if (Date.now() - Date.parse(d.last_success) > d.max_stale_seconds * 1000)
      return { label: "Устарела", color: "warning" as const };
    return { label: "Актуальна", color: "success" as const };
  };
  return (
    <ThemeProvider theme={theme}>
      <CssBaseline />
      {auth === null ? (
        <Box className="center-screen">
          <CircularProgress />
        </Box>
      ) : !auth ? (
        <Login onLogin={() => setAuth(true)} />
      ) : (
        <Box sx={{ display: "flex", minHeight: "100vh" }}>
          <Drawer
            variant="permanent"
            sx={{
              display: { xs: "none", lg: "block" },
              width: 260,
              flexShrink: 0,
              "& .MuiDrawer-paper": {
                width: 260,
                borderRight: "1px solid",
                borderColor: "divider",
                bgcolor: "background.default",
              },
            }}
          >
            {sidebar}
          </Drawer>
          <Drawer
            open={mobile}
            onClose={() => setMobile(false)}
            sx={{ "& .MuiDrawer-paper": { width: 280 } }}
          >
            {sidebar}
          </Drawer>
          <Box component="main" sx={{ flex: 1, minWidth: 0 }}>
            <AppBar
              position="sticky"
              elevation={0}
              sx={{
                bgcolor: "background.default",
                color: "text.primary",
                backdropFilter: "blur(20px)",
                borderBottom: 1,
                borderColor: "divider",
              }}
            >
              <Toolbar sx={{ gap: 1, px: { xs: 2, md: 4 } }}>
                <IconButton
                  aria-label="Открыть меню"
                  onClick={() => setMobile(true)}
                  sx={{ display: { lg: "none" } }}
                >
                  <MenuRounded />
                </IconButton>
                <Box sx={{ flex: 1 }}>
                  <Stack direction="row" spacing={1} alignItems="center">
                    <Typography variant="body2" color="text.secondary">
                      Пространство
                    </Typography>
                    <Typography variant="body2">/</Typography>
                    <Typography variant="body2" fontWeight={600}>
                      {pages.find((p) => p[0] === page)?.[1] || "Обзор"}
                    </Typography>
                  </Stack>
                </Box>
                <Tooltip title="Фоновые операции">
                  <Button
                    onClick={() => setActivity(true)}
                    color="inherit"
                    startIcon={
                      pending.length ? (
                        <CircularProgress size={15} />
                      ) : (
                        <CheckCircleOutlineRounded />
                      )
                    }
                    sx={{ display: { xs: "none", sm: "flex" } }}
                  >
                    {pending.length ? `${pending.length} в работе` : "Операции"}
                  </Button>
                </Tooltip>
                <Tooltip
                  title={mode === "dark" ? "Светлая тема" : "Тёмная тема"}
                >
                  <IconButton
                    aria-label="Переключить тему"
                    onClick={() =>
                      setMode((v) => {
                        const next = v === "dark" ? "light" : "dark";
                        localStorage.setItem("theme", next);
                        return next;
                      })
                    }
                  >
                    {mode === "dark" ? (
                      <LightModeRounded />
                    ) : (
                      <DarkModeRounded />
                    )}
                  </IconButton>
                </Tooltip>
                <Tooltip title="Выйти">
                  <IconButton
                    aria-label="Выйти"
                    onClick={async () => {
                      await api("auth/logout", "POST", {});
                      setAuth(false);
                    }}
                  >
                    <LogoutRounded />
                  </IconButton>
                </Tooltip>
              </Toolbar>
            </AppBar>
            {loading && (
              <LinearProgress
                sx={{
                  height: 2,
                  position: "fixed",
                  zIndex: 1400,
                  width: "100%",
                }}
              />
            )}
            <Box
              sx={{ p: { xs: 2, sm: 3, md: 4 }, maxWidth: 1700, mx: "auto" }}
            >
              <Stack
                direction={{ xs: "column", sm: "row" }}
                justifyContent="space-between"
                spacing={2}
                mb={3.5}
              >
                <Box>
                  <Stack direction="row" alignItems="center" gap={1.5}>
                    <Typography variant="h4">
                      {pages.find((p) => p[0] === page)?.[1]}
                    </Typography>
                    {labels[page] && (
                      <Chip
                        size="small"
                        label={items.length}
                        variant="outlined"
                      />
                    )}
                  </Stack>
                  <Typography color="text.secondary" mt={1} variant="body2">
                    {subtitles[page]}
                  </Typography>
                </Box>
                <Stack direction="row" spacing={1} alignItems="center">
                  <Tooltip title="Обновить данные">
                    <IconButton
                      aria-label="Обновить данные"
                      onClick={() => load()}
                    >
                      <RefreshRounded />
                    </IconButton>
                  </Tooltip>
                  {labels[page] && page !== "profiles" && (
                    <Button
                      variant="outlined"
                      startIcon={<AddRounded />}
                      onClick={() => openEditor(page)}
                    >
                      Добавить
                    </Button>
                  )}
                  <Button
                    variant="contained"
                    startIcon={<PlayArrowRounded />}
                    onClick={async () => {
                      try {
                        setPlan(await api("config/plan", "POST", {}));
                      } catch (e) {
                        notify(message(e), "error");
                      }
                    }}
                  >
                    Применить{dirty ? " *" : ""}
                  </Button>
                </Stack>
              </Stack>
              {error && (
                <Alert
                  severity="error"
                  action={<Button onClick={() => load()}>Повторить</Button>}
                  sx={{ mb: 3 }}
                >
                  {error}
                </Alert>
              )}
              <Box key={page} className="page-enter">
                {overview?.restore_pending && (
                  <Alert severity="info" sx={{ mb: 3 }}>
                    Резервная копия восстановлена в настройки. Проверьте план и
                    нажмите «Применить»: автоматическое обновление активных
                    маршрутов приостановлено до успешного применения.
                  </Alert>
                )}
                {page === "topology" && <TrafficTopology navigate={navigate} />}
                {page === "connections" && <ClientAccess />}
                {page === "overview" && overview && (
                  <>
                    <Box className="hero-panel">
                      <Box sx={{ maxWidth: 660 }}>
                        <Chip
                          icon={<AutoAwesomeRounded />}
                          label="Умная маршрутизация"
                          variant="outlined"
                          color="primary"
                          size="small"
                        />
                        <Typography variant="h4" mt={2.5} mb={1}>
                          Пути меняются.
                          <br />
                          Соединения остаются.
                        </Typography>
                        <Typography
                          color="text.secondary"
                          sx={{ maxWidth: 520 }}
                        >
                          Подписки, проверенные маршруты и локальное
                          восстановление. Одна точка подключения для всей
                          домашней сети.
                        </Typography>
                        <Stack direction="row" gap={1.5} mt={3} flexWrap="wrap">
                          <Button
                            variant="contained"
                            onClick={() =>
                              navigate(
                                overview.counts.sources
                                  ? "listeners"
                                  : "sources",
                              )
                            }
                            endIcon={<ArrowForwardRounded />}
                          >
                            {overview.counts.sources
                              ? "Подключить устройства"
                              : "Добавить первую подписку"}
                          </Button>
                          {grafanaURL && (
                            <Button
                              startIcon={<BarChartRounded />}
                              href={grafanaURL}
                              target="_blank"
                              rel="noopener noreferrer"
                            >
                              Открыть Grafana
                            </Button>
                          )}
                        </Stack>
                      </Box>
                      <Box className="network-art" aria-hidden="true">
                        <Box className="art-ring ring-a" />
                        <Box className="art-ring ring-b" />
                        <Box className="art-center">
                          <HubRounded sx={{ fontSize: 48 }} />
                        </Box>
                        <Box className="art-node node-a">
                          <BoltRounded />
                        </Box>
                        <Box className="art-node node-b">
                          <ShieldRounded />
                        </Box>
                        <Box className="art-node node-c">
                          <AltRouteRounded />
                        </Box>
                      </Box>
                    </Box>
                    <Box className="stat-grid" my={3}>
                      {[
                        {
                          label: "Подписки",
                          value: overview.counts.sources,
                          detail: countRu(
                            overview.sources.fresh,
                            "актуальная",
                            "актуальные",
                            "актуальных",
                          ),
                          icon: LinkRounded,
                        },
                        {
                          label: "Профили",
                          value: overview.counts.profiles,
                          detail: countRu(
                            overview.counts.pools,
                            "пул",
                            "пула",
                            "пулов",
                          ),
                          icon: AltRouteRounded,
                        },
                        {
                          label: "Соединения",
                          value: overview.workers.reduce(
                            (n: number, w: any) =>
                              n + (w.status.active_connections || 0),
                            0,
                          ),
                          detail: "TCP и управляющие UDP-сессии",
                          icon: SwapHorizRounded,
                        },
                        {
                          label: "Воркеры",
                          value: `${overview.workers.filter((w: any) => w.online).length} / ${overview.workers.length}`,
                          detail: "На связи",
                          icon: StorageRounded,
                        },
                      ].map(({ label, value, detail, icon: Icon }) => (
                        <Card key={label}>
                          <CardContent>
                            <Stack
                              direction="row"
                              justifyContent="space-between"
                              mb={2}
                            >
                              <Typography
                                color="text.secondary"
                                variant="body2"
                              >
                                {label}
                              </Typography>
                              <Icon color="primary" />
                            </Stack>
                            <Typography variant="h4" mb={0.5}>
                              {value}
                            </Typography>
                            <Typography
                              variant="caption"
                              color="text.secondary"
                            >
                              {detail}
                            </Typography>
                          </CardContent>
                        </Card>
                      ))}
                    </Box>
                    {(overview.sources.errors > 0 ||
                      overview.sources.stale > 0) && (
                      <Alert
                        severity="warning"
                        sx={{ mb: 3 }}
                        action={
                          <Button onClick={() => navigate("sources")}>
                            Проверить
                          </Button>
                        }
                      >
                        {overview.sources.errors} подписок с ошибкой,{" "}
                        {overview.sources.stale} без актуального снимка.
                        Устаревшие профили не получают новые соединения.
                      </Alert>
                    )}
                    <Box className="two-col wide">
                      <Card>
                        <CardContent>
                          <Typography variant="h6" mb={2}>
                            Маршруты и автономность
                          </Typography>
                          {overview.workers.length === 0 ? (
                            <Empty
                              title="Ожидаем воркер"
                              text="Сервис подключится к управляющему API с помощью mTLS."
                            />
                          ) : (
                            overview.workers.map((w: any) => (
                              <Box key={w.id} mb={2}>
                                <Stack
                                  direction="row"
                                  justifyContent="space-between"
                                >
                                  <Typography fontWeight={650}>
                                    {w.id}
                                  </Typography>
                                  <Chip
                                    size="small"
                                    color={w.online ? "success" : "warning"}
                                    label={
                                      w.online
                                        ? "На связи"
                                        : "Автономно / недоступен"
                                    }
                                  />
                                </Stack>
                                <Typography
                                  color="text.secondary"
                                  variant="body2"
                                  mt={1}
                                >
                                  Ревизия {w.generation} ·{" "}
                                  {countRu(
                                    w.status.cores?.filter((c: any) => c.ready)
                                      .length || 0,
                                    "готовое ядро",
                                    "готовых ядра",
                                    "готовых ядер",
                                  )}{" "}
                                  ·{" "}
                                  {countRu(
                                    w.status.udp_associations || 0,
                                    "UDP-ассоциация",
                                    "UDP-ассоциации",
                                    "UDP-ассоциаций",
                                  )}
                                </Typography>
                                <Typography
                                  color="text.secondary"
                                  variant="caption"
                                >
                                  Обновлено {ago(w.seen_at)} · аренда до{" "}
                                  {w.status.lease_until
                                    ? new Date(
                                        w.status.lease_until * 1000,
                                      ).toLocaleString()
                                    : "получения конфигурации"}
                                </Typography>
                              </Box>
                            ))
                          )}
                        </CardContent>
                      </Card>
                      <Card>
                        <CardContent>
                          <Typography variant="h6" mb={2}>
                            Быстрый старт
                          </Typography>
                          {[
                            ["1", "Добавьте подписки", "sources"],
                            ["2", "Соберите агрегат и пул", "pools"],
                            ["3", "Выберите политику", "policies"],
                            ["4", "Создайте HTTP / SOCKS5 вход", "listeners"],
                          ].map(([n, text, id]) => (
                            <Box
                              key={n}
                              onClick={() => navigate(id)}
                              className="quick-step"
                            >
                              <Avatar
                                sx={{
                                  width: 30,
                                  height: 30,
                                  fontSize: 14,
                                  bgcolor: "action.selected",
                                  color: "primary.main",
                                }}
                              >
                                {n}
                              </Avatar>
                              <Typography variant="body2" sx={{ flex: 1 }}>
                                {text}
                              </Typography>
                              <ArrowForwardRounded
                                sx={{ fontSize: 18, color: "text.secondary" }}
                              />
                            </Box>
                          ))}
                        </CardContent>
                      </Card>
                    </Box>
                  </>
                )}
                {labels[page] && (
                  <>
                    <TextField
                      label="Поиск"
                      placeholder={
                        page === "profiles"
                          ? "Название, источник, страна, протокол или адрес"
                          : "Название"
                      }
                      value={search}
                      onChange={(e) => setSearch(e.target.value)}
                      sx={{ mb: 3, minWidth: { xs: "100%", sm: 360 } }}
                      slotProps={{
                        input: {
                          startAdornment: (
                            <InputAdornment position="start">
                              <SearchRounded />
                            </InputAdornment>
                          ),
                        },
                      }}
                    />
                    {page === "profiles" ? (
                      <Card sx={{ height: 660 }}>
                        <DataGrid
                          rows={filtered}
                          columns={dataColumns}
                          disableRowSelectionOnClick
                          pageSizeOptions={[25, 50, 100]}
                          initialState={{
                            pagination: { paginationModel: { pageSize: 50 } },
                          }}
                          localeText={{
                            noRowsLabel: "Профилей пока нет",
                            noResultsOverlayLabel: "Ничего не найдено",
                          }}
                          rowHeight={62}
                        />
                      </Card>
                    ) : filtered.length === 0 && !loading ? (
                      <Empty
                        title={
                          search
                            ? "Ничего не найдено"
                            : `Здесь появятся ${labels[page].toLowerCase()}`
                        }
                        text={
                          search
                            ? "Попробуйте другой запрос."
                            : "Добавьте первый объект, чтобы продолжить настройку."
                        }
                        action={
                          !search ? (
                            <Button
                              variant="contained"
                              startIcon={<AddRounded />}
                              onClick={() => openEditor(page)}
                            >
                              Добавить
                            </Button>
                          ) : undefined
                        }
                      />
                    ) : (
                      <Box className="card-grid">
                        {filtered.map((item) => (
                          <Card key={item.id} className="object-card">
                            <CardContent sx={{ flex: 1 }}>
                              <Stack
                                direction="row"
                                gap={1.5}
                                alignItems="start"
                                justifyContent="space-between"
                              >
                                <Avatar
                                  sx={{
                                    bgcolor: "action.selected",
                                    color: "primary.main",
                                    borderRadius: 3,
                                  }}
                                >
                                  {page === "sources" ? (
                                    <LinkRounded />
                                  ) : page === "aggregates" ? (
                                    <HubRounded />
                                  ) : page === "pools" ? (
                                    <FilterAltRounded />
                                  ) : page === "policies" ? (
                                    <AutoAwesomeRounded />
                                  ) : (
                                    <SwapHorizRounded />
                                  )}
                                </Avatar>
                                <Box sx={{ flex: 1, minWidth: 0 }}>
                                  <Typography variant="h6" noWrap>
                                    {item.name}
                                  </Typography>
                                  <Typography
                                    variant="caption"
                                    color="text.secondary"
                                  >
                                    Обновлено {ago(item.updated_at)}
                                  </Typography>
                                </Box>
                                <Tooltip title="Редактировать">
                                  <IconButton
                                    aria-label={"Редактировать " + item.name}
                                    onClick={() => openEditor(page, item)}
                                  >
                                    <EditRounded fontSize="small" />
                                  </IconButton>
                                </Tooltip>
                              </Stack>
                              {page === "sources" && (
                                <>
                                  <Typography
                                    variant="body2"
                                    color="text.secondary"
                                    sx={{ mt: 2, overflowWrap: "anywhere" }}
                                  >
                                    {item.data.url_hint || "URL скрыт"}
                                  </Typography>
                                  <Stack
                                    direction="row"
                                    alignItems="center"
                                    gap={1}
                                    mt={2}
                                  >
                                    <Chip
                                      size="small"
                                      label={sourceStatus(item).label}
                                      color={sourceStatus(item).color}
                                      variant="outlined"
                                    />
                                    <Typography variant="body2">
                                      {countRu(
                                        item.data.profile_count || 0,
                                        "профиль",
                                        "профиля",
                                        "профилей",
                                      )}
                                    </Typography>
                                  </Stack>
                                  <Typography
                                    variant="caption"
                                    color="text.secondary"
                                    display="block"
                                    mt={1.5}
                                  >
                                    Успешно: {ago(item.data.last_success)} ·
                                    Xray {item.data.core_version}
                                  </Typography>
                                  {item.data.last_error && (
                                    <Alert
                                      severity={
                                        item.data.pending_snapshot
                                          ? "warning"
                                          : "error"
                                      }
                                      sx={{ mt: 2 }}
                                    >
                                      {message(new Error(item.data.last_error))}{" "}
                                      {item.data.last_success &&
                                      !item.data.last_success.startsWith(
                                        "0001-",
                                      )
                                        ? "Предыдущий каталог сохранён."
                                        : "Успешной загрузки ещё не было."}
                                    </Alert>
                                  )}
                                  {item.data.quota?.total > 0 && (
                                    <Box mt={2}>
                                      <LinearProgress
                                        variant="determinate"
                                        value={Math.min(
                                          100,
                                          (((item.data.quota.upload || 0) +
                                            (item.data.quota.download || 0)) /
                                            item.data.quota.total) *
                                            100,
                                        )}
                                      />
                                      <Typography
                                        variant="caption"
                                        color="text.secondary"
                                      >
                                        Квота:{" "}
                                        {Math.round(
                                          ((item.data.quota.upload || 0) +
                                            (item.data.quota.download || 0)) /
                                            1073741824,
                                        )}{" "}
                                        /{" "}
                                        {Math.round(
                                          item.data.quota.total / 1073741824,
                                        )}{" "}
                                        ГиБ
                                      </Typography>
                                    </Box>
                                  )}
                                </>
                              )}
                              {page === "aggregates" && (
                                <Stack gap={1} mt={2}>
                                  <Typography
                                    color="text.secondary"
                                    variant="body2"
                                  >
                                    Подписок:{" "}
                                    {item.data.source_ids?.length || 0}
                                  </Typography>
                                  <Stack
                                    direction="row"
                                    gap={1}
                                    flexWrap="wrap"
                                  >
                                    <Chip
                                      size="small"
                                      label={
                                        item.data.dedupe
                                          ? "Без точных дублей"
                                          : "С дублями"
                                      }
                                    />
                                    {item.data.prefix_names && (
                                      <Chip
                                        size="small"
                                        label="Префикс подписки"
                                      />
                                    )}
                                  </Stack>
                                </Stack>
                              )}
                              {page === "pools" && (
                                <Stack gap={1.5} mt={2}>
                                  <Typography
                                    color="text.secondary"
                                    variant="body2"
                                  >
                                    {item.data.profile_ids?.length
                                      ? `Ручной список: ${countRu(item.data.profile_ids.length, "профиль", "профиля", "профилей")}`
                                      : "Автоматическое включение новых профилей"}
                                  </Typography>
                                  <Stack
                                    direction="row"
                                    gap={1}
                                    flexWrap="wrap"
                                  >
                                    <Chip
                                      size="small"
                                      color={
                                        item.data.enabled
                                          ? "success"
                                          : "default"
                                      }
                                      variant="outlined"
                                      label={
                                        item.data.enabled
                                          ? "Включён"
                                          : "Выключен"
                                      }
                                    />
                                    <Chip
                                      size="small"
                                      label={`Условий: ${item.data.filter?.conditions?.length || 0}`}
                                    />
                                    <Chip
                                      size="small"
                                      label={`Исключено: ${item.data.filter?.excluded_ids?.length || 0}`}
                                    />
                                  </Stack>
                                </Stack>
                              )}
                              {page === "policies" && (
                                <Stack gap={1.5} mt={2}>
                                  <Chip
                                    sx={{ alignSelf: "start" }}
                                    color="primary"
                                    variant="outlined"
                                    label={
                                      modeLabels[item.data.mode] ||
                                      item.data.mode
                                    }
                                  />
                                  <Typography
                                    variant="body2"
                                    color="text.secondary"
                                  >
                                    {item.data.active_paths} активных ·{" "}
                                    {item.data.warm_reserve} резервных
                                  </Typography>
                                  <Typography
                                    variant="caption"
                                    color="text.secondary"
                                  >
                                    Проверка каждые{" "}
                                    {item.data.probe_interval_seconds} с ·
                                    Бюджет{" "}
                                    {item.data.probe_budget_bytes / 1048576}{" "}
                                    МиБ/ч
                                  </Typography>
                                </Stack>
                              )}
                              {page === "listeners" && (
                                <Stack gap={1.5} mt={2}>
                                  <Stack direction="row" gap={1}>
                                    <Chip
                                      size="small"
                                      label={
                                        item.data.protocol === "http"
                                          ? "HTTP / CONNECT"
                                          : "SOCKS5"
                                      }
                                    />
                                    {item.data.udp && (
                                      <Chip
                                        size="small"
                                        label="TCP + UDP"
                                        color="secondary"
                                        variant="outlined"
                                      />
                                    )}
                                  </Stack>
                                  <Typography
                                    fontFamily="monospace"
                                    variant="body2"
                                  >
                                    {item.data.listen}
                                  </Typography>
                                  <Typography
                                    variant="body2"
                                    color="text.secondary"
                                  >
                                    {item.data.username
                                      ? "Аутентификация включена"
                                      : "Без аутентификации"}{" "}
                                    ·{" "}
                                    {item.data.enabled
                                      ? "Включена"
                                      : "Выключена"}
                                  </Typography>
                                </Stack>
                              )}
                            </CardContent>
                            <CardActions
                              sx={{
                                px: 2.5,
                                pb: 2,
                                display: "grid",
                                gridTemplateColumns: "minmax(0, 1fr) auto",
                                alignItems: "start",
                                gap: 1,
                              }}
                            >
                              <Stack
                                direction="row"
                                spacing={0.5}
                                useFlexGap
                                flexWrap="wrap"
                                sx={{ minWidth: 0 }}
                              >
                                {page === "sources" && (
                                  <>
                                    <Button
                                      size="small"
                                      startIcon={<RefreshRounded />}
                                      onClick={() =>
                                        operate(`sources/${item.id}/refresh`)
                                      }
                                    >
                                      Обновить
                                    </Button>
                                    <Button
                                      size="small"
                                      onClick={() =>
                                        operate(`sources/${item.id}/test`)
                                      }
                                    >
                                      Проверить
                                    </Button>
                                    {item.data.pending_snapshot && (
                                      <Button
                                        size="small"
                                        color="warning"
                                        onClick={() =>
                                          setConfirm({
                                            title:
                                              "Принять сокращённую подписку?",
                                            text: "Состав источника существенно уменьшился. После принятия пропавшие профили перестанут получать новые соединения.",
                                            action: async () => {
                                              await operate(
                                                `sources/${item.id}/approve`,
                                              );
                                            },
                                          })
                                        }
                                      >
                                        Принять
                                      </Button>
                                    )}
                                  </>
                                )}
                                {(page === "sources" || page === "pools") && (
                                  <Tooltip title="Посмотреть итоговый состав">
                                    <IconButton
                                      aria-label={"Предпросмотр " + item.name}
                                      onClick={() =>
                                        openPreview(
                                          `${page}/${item.id}/preview`,
                                        )
                                      }
                                    >
                                      <VisibilityRounded fontSize="small" />
                                    </IconButton>
                                  </Tooltip>
                                )}
                              </Stack>
                              <Tooltip title="Удалить">
                                <IconButton
                                  aria-label={"Удалить " + item.name}
                                  onClick={() => deleteItem(item)}
                                >
                                  <DeleteOutlineRounded fontSize="small" />
                                </IconButton>
                              </Tooltip>
                            </CardActions>
                          </Card>
                        ))}
                      </Box>
                    )}
                  </>
                )}
                {page === "cores" && (
                  <Stack spacing={3}>
                    <Alert severity="info">
                      Основная версия включена в образ. Дополнительные версии
                      загружаются с официального GitHub XTLS, проверяются по
                      SHA-256 и запускаются отдельно. Назначить версию можно
                      подписке или конкретному профилю.
                    </Alert>
                    <CoreInstall
                      install={(v) => operate("cores/install", { version: v })}
                    />
                    <CoreApproval
                      approve={(value) => operate("cores/approve", value)}
                    />
                    <CoreCache />
                    <Box className="card-grid">
                      {other.items?.map((a: any) => (
                        <Card key={a.version + "-" + (a.variant || "")}>
                          <CardContent>
                            <Stack
                              direction="row"
                              justifyContent="space-between"
                            >
                              <Avatar
                                sx={{
                                  bgcolor: "action.selected",
                                  color: "primary.main",
                                }}
                              >
                                <BoltRounded />
                              </Avatar>
                              <Chip
                                size="small"
                                label={
                                  a.version === other.builtin && !a.variant
                                    ? "В образе"
                                    : "Одобрена"
                                }
                                color={
                                  a.version === other.builtin && !a.variant
                                    ? "primary"
                                    : "default"
                                }
                              />
                            </Stack>
                            <Typography variant="h5" mt={2}>
                              Xray {a.version}
                              {a.variant ? "-" + a.variant : ""}
                            </Typography>
                            <Typography
                              color="text.secondary"
                              variant="body2"
                              mt={1}
                            >
                              Linux {a.arch}
                            </Typography>
                            {a.version === "26.9.30" && (
                              <Alert severity="warning" sx={{ mt: 2 }}>
                                Версия для проверки совместимости. Аудит
                                исходного релиза выявил устаревшие зависимости.
                                Для обычного использования выбирайте основную
                                версию.
                              </Alert>
                            )}
                            <Typography
                              variant="caption"
                              color="text.secondary"
                              component="div"
                              sx={{ mt: 2, wordBreak: "break-all" }}
                            >
                              SHA-256 архива
                              <br />
                              {a.sha256}
                            </Typography>
                          </CardContent>
                        </Card>
                      ))}
                    </Box>
                    <Typography color="text.secondary" variant="body2">
                      Поддержка других движков предусмотрена границей адаптера.
                      Текущий исполняемый адаптер: Xray. Произвольный
                      исполняемый файл через API не принимается.
                    </Typography>
                  </Stack>
                )}
                {page === "workers" && (
                  <Stack spacing={3}>
                    {other.items?.length === 0 ? (
                      <Empty
                        title="Воркер пока не подключился"
                        text="Проверьте Compose и сертификаты mTLS."
                      />
                    ) : (
                      other.items?.map((w: any) => (
                        <Card key={w.id}>
                          <CardContent>
                            <Stack
                              direction="row"
                              justifyContent="space-between"
                              flexWrap="wrap"
                              gap={2}
                            >
                              <Box>
                                <Typography variant="h6">{w.id}</Typography>
                                <Typography
                                  variant="body2"
                                  color="text.secondary"
                                >
                                  Ревизия {w.generation} · Обновлено{" "}
                                  {ago(w.seen_at)}
                                </Typography>
                              </Box>
                              <Stack direction="row" spacing={1}>
                                <Chip
                                  size="small"
                                  color={w.online ? "success" : "warning"}
                                  label={w.online ? "На связи" : "Недоступен"}
                                />
                                <Button
                                  size="small"
                                  startIcon={
                                    w.status.drained ? (
                                      <PlayArrowRounded />
                                    ) : (
                                      <PauseRounded />
                                    )
                                  }
                                  onClick={() =>
                                    operate(
                                      `workers/${w.id}/${w.status.drained ? "resume" : "drain"}`,
                                      {},
                                    )
                                  }
                                >
                                  {w.status.drained
                                    ? "Возобновить"
                                    : "Дренировать"}
                                </Button>
                              </Stack>
                            </Stack>
                            <Typography
                              color="text.secondary"
                              variant="body2"
                              my={2}
                            >
                              {countRu(
                                w.status.catalog_size || 0,
                                "профиль",
                                "профиля",
                                "профилей",
                              )}{" "}
                              в каталоге ·{" "}
                              {countRu(
                                w.status.cores?.length || 0,
                                "процесс",
                                "процесса",
                                "процессов",
                              )}{" "}
                              ·{" "}
                              {countRu(
                                w.status.active_connections || 0,
                                "соединение",
                                "соединения",
                                "соединений",
                              )}{" "}
                              · {w.status.udp_associations || 0} UDP
                            </Typography>
                            {w.status.failed_generation > 0 && (
                              <Alert severity="error" sx={{ mb: 2 }}>
                                Не принята ревизия {w.status.failed_generation}.
                                Проверьте причину отказа в журнале воркера.
                              </Alert>
                            )}
                            <Box
                              sx={{
                                height: Math.min(
                                  440,
                                  110 + (w.status.cores?.length || 0) * 58,
                                ),
                              }}
                            >
                              <DataGrid
                                rows={w.status.cores || []}
                                columns={[
                                  {
                                    field: "id",
                                    headerName: "Профиль / процесс",
                                    minWidth: 220,
                                    flex: 1,
                                  },
                                  {
                                    field: "version",
                                    headerName: "Xray",
                                    width: 110,
                                  },
                                  {
                                    field: "ready",
                                    headerName: "Состояние",
                                    width: 130,
                                    renderCell: (p) => (
                                      <Chip
                                        size="small"
                                        color={p.value ? "success" : "warning"}
                                        label={
                                          p.value ? "Готов" : "Проверяется"
                                        }
                                        variant="outlined"
                                      />
                                    ),
                                  },
                                  {
                                    field: "active",
                                    headerName: "Соединения",
                                    width: 115,
                                  },
                                  {
                                    field: "rtt_ms",
                                    headerName: "Подключение, мс",
                                    width: 145,
                                    valueFormatter: (v: any) =>
                                      v ? Number(v).toFixed(1) : "нет замера",
                                  },
                                  {
                                    field: "restarts",
                                    headerName: "Перезапуски",
                                    width: 125,
                                  },
                                  {
                                    field: "action",
                                    headerName: "",
                                    width: 60,
                                    renderCell: (p) => (
                                      <Tooltip title="Перезапустить ядро">
                                        <IconButton
                                          aria-label={
                                            "Перезапустить " + p.row.id
                                          }
                                          onClick={() =>
                                            setConfirm({
                                              title: "Перезапустить ядро?",
                                              text: `Активных соединений у процесса: ${p.row.active || 0}. Они будут закрыты. Другие процессы продолжат работу.`,
                                              action: async () => {
                                                await operate(
                                                  `workers/${w.id}/restart-core`,
                                                  { route_id: p.row.id },
                                                );
                                              },
                                            })
                                          }
                                        >
                                          <RestartAltRounded />
                                        </IconButton>
                                      </Tooltip>
                                    ),
                                  },
                                ]}
                                hideFooter
                                disableRowSelectionOnClick
                                rowHeight={58}
                              />
                            </Box>
                          </CardContent>
                        </Card>
                      ))
                    )}
                  </Stack>
                )}
                {page === "events" && <EventList items={other.items || []} />}
                {page === "settings" && items[0] && (
                  <Settings
                    item={items[0]}
                    other={other}
                    save={async (data) => {
                      await api(
                        "settings/global",
                        "PATCH",
                        { data },
                        items[0].version,
                      );
                      setDirty(true);
                      load(true);
                      notify("Настройки сохранены. Примените конфигурацию.");
                    }}
                    notify={notify}
                    operate={operate}
                    refresh={() => load(true)}
                    confirm={setConfirm}
                  />
                )}
                {page === "help" && <Help />}
              </Box>
              <Typography
                variant="caption"
                color="text.secondary"
                component="footer"
                sx={{ mt: 5, textAlign: "center", opacity: 0.65 }}
              >
                RelayWeave · Локальное управление ·{" "}
                {overview?.server_time
                  ? `Сервер: ${new Date(overview.server_time).toLocaleTimeString()}`
                  : "Подключение…"}
              </Typography>
            </Box>
          </Box>
          {editor && (
            <Editor
              kind={editor.kind}
              item={editor.item}
              catalog={catalog}
              onClose={() => setEditor(null)}
              onSaved={() => {
                const live = ["sources", "profiles"].includes(editor.kind);
                setEditor(null);
                if (!live) setDirty(true);
                load(true);
                notify(
                  live
                    ? "Сохранено. Действующие маршруты обновляются в фоне."
                    : "Сохранено. Примените конфигурацию, чтобы изменить маршруты.",
                );
              }}
            />
          )}
          <Dialog
            open={!!confirm}
            onClose={() => setConfirm(null)}
            maxWidth="sm"
            fullWidth
          >
            <DialogTitle>{confirm?.title}</DialogTitle>
            <DialogContent>
              <Typography color="text.secondary">{confirm?.text}</Typography>
            </DialogContent>
            <DialogActions sx={{ p: 2.5 }}>
              <Button onClick={() => setConfirm(null)}>Отмена</Button>
              <Button
                variant="contained"
                onClick={async () => {
                  const action = confirm?.action;
                  setConfirm(null);
                  try {
                    await action?.();
                  } catch (e) {
                    notify(message(e), "error");
                  }
                }}
              >
                Подтвердить
              </Button>
            </DialogActions>
          </Dialog>
          <Dialog
            open={!!plan}
            onClose={() => setPlan(null)}
            maxWidth="sm"
            fullWidth
          >
            <DialogTitle>Применить конфигурацию</DialogTitle>
            <DialogContent>
              <Alert
                severity={plan?.routes ? "success" : "warning"}
                sx={{ mb: 3 }}
              >
                {plan?.routes
                  ? "Проверка структуры пройдена. Воркер проверит реальные маршруты перед переключением."
                  : "Нет разрешённых маршрутов. После применения новые соединения будут отклоняться."}
              </Alert>
              <Stack direction="row" justifyContent="space-around" mb={3}>
                {[
                  ["Профилей", plan?.routes],
                  ["Версий Xray", plan?.cores],
                  ["Точек входа", plan?.listeners],
                ].map(([text, value]) => (
                  <Box key={text} textAlign="center">
                    <Typography variant="h4">{value}</Typography>
                    <Typography color="text.secondary" variant="caption">
                      {text}
                    </Typography>
                  </Box>
                ))}
              </Stack>
              <Typography color="text.secondary" variant="body2">
                Открытые сеансы сохраняются. При неудачном применении остаются
                исправные разрешённые пути. Отозванные профили, старые пароли и
                прежние ACL не возвращаются.
              </Typography>
            </DialogContent>
            <DialogActions sx={{ p: 2.5 }}>
              <Button onClick={() => setPlan(null)}>Отмена</Button>
              <Button
                variant="contained"
                onClick={() => {
                  setPlan(null);
                  setDirty(false);
                  operate("config/apply");
                }}
              >
                Применить
              </Button>
            </DialogActions>
          </Dialog>
          <Dialog
            open={!!preview}
            onClose={closePreview}
            maxWidth="lg"
            fullWidth
          >
            <DialogTitle>Предпросмотр состава</DialogTitle>
            <DialogContent>
              {preview?.diff && (
                <Stack spacing={1.5} mb={2}>
                  <Alert
                    severity={preview.requires_review ? "warning" : "info"}
                  >
                    {preview.requires_review
                      ? "Предложенный состав ещё не принят. "
                      : "Сравнение с текущим составом. "}
                    Добавлено: {preview.diff.added} · Удалено:{" "}
                    {preview.diff.removed} · Изменено подключений:{" "}
                    {preview.diff.credentials_changed} · Транспорт и протокол:{" "}
                    {preview.diff.parameters_changed || 0} · Сохранено:{" "}
                    {preview.diff.retained}
                    {preview.diff.ambiguous > 0
                      ? ` · Неоднозначных совпадений: ${preview.diff.ambiguous}`
                      : ""}
                  </Alert>
                  <Box component="details">
                    <Box
                      component="summary"
                      sx={{ cursor: "pointer", py: 1.5, color: "primary.main" }}
                    >
                      Показать изменённые профили
                    </Box>
                    <Box className="two-col">
                      {[
                        ["Добавлены", preview.diff.added_items],
                        ["Удалены", preview.diff.removed_items],
                        ["Изменены", preview.diff.changed_items],
                      ].map(([title, rows]: any) => (
                        <Box key={title}>
                          <Typography variant="subtitle2">{title}</Typography>
                          {rows.length === 0 ? (
                            <Typography
                              variant="caption"
                              color="text.secondary"
                            >
                              Нет изменений
                            </Typography>
                          ) : (
                            rows.map((v: any, i: number) => (
                              <Typography
                                key={i}
                                variant="body2"
                                color="text.secondary"
                                sx={{ py: 0.5, overflowWrap: "anywhere" }}
                              >
                                {v.name} · {v.protocol} · {v.host}:{v.port}
                              </Typography>
                            ))
                          )}
                        </Box>
                      ))}
                    </Box>
                  </Box>
                </Stack>
              )}
              <Typography color="text.secondary" mb={2}>
                Всего: {preview?.total || 0}{" "}
                {preview?.included !== undefined
                  ? `· Включено: ${preview.included} · Исключено: ${preview.excluded}`
                  : ""}
              </Typography>
              {preview?.truncated && (
                <Alert severity="info" sx={{ mb: 2 }}>
                  Показаны первые{" "}
                  {countRu(
                    preview.items.length,
                    "профиль",
                    "профиля",
                    "профилей",
                  )}
                  . Итоговые счётчики учитывают весь источник; полный каталог
                  доступен в разделе «Все профили».
                </Alert>
              )}
              <Box sx={{ height: 480 }}>
                <DataGrid
                  loading={previewBusy}
                  paginationMode={preview?._path ? "server" : "client"}
                  rowCount={preview?._path ? preview.total : undefined}
                  paginationModel={
                    preview?._path
                      ? {
                          page: Math.floor(
                            (preview.offset || 0) / (preview.limit || 25),
                          ),
                          pageSize: preview.limit || 25,
                        }
                      : undefined
                  }
                  onPaginationModelChange={(model) => {
                    if (
                      preview?._path &&
                      (model.page !==
                        Math.floor(
                          (preview.offset || 0) / (preview.limit || 25),
                        ) ||
                        model.pageSize !== preview.limit)
                    )
                      openPreview(preview._path, model.page, model.pageSize);
                  }}
                  rows={(preview?.items || []).map((v: any, i: number) => ({
                    id: v.id || String(i),
                    name: v.name || v.data?.name,
                    ...v,
                    ...v.data,
                  }))}
                  columns={[
                    {
                      field: "name",
                      headerName: "Название",
                      flex: 1,
                      minWidth: 240,
                    },
                    { field: "protocol", headerName: "Протокол", width: 110 },
                    {
                      field: "host",
                      headerName: "Сервер",
                      flex: 1,
                      minWidth: 160,
                    },
                    { field: "country", headerName: "Страна", width: 90 },
                    {
                      field: "reason",
                      headerName: "Результат фильтра",
                      width: 210,
                    },
                  ]}
                  pageSizeOptions={[25, 50, 100]}
                  initialState={{
                    pagination: { paginationModel: { pageSize: 25 } },
                  }}
                />
              </Box>
            </DialogContent>
            <DialogActions>
              <Button onClick={closePreview}>Закрыть</Button>
            </DialogActions>
          </Dialog>
          <Drawer
            anchor="right"
            open={activity}
            onClose={() => setActivity(false)}
            sx={{
              "& .MuiDrawer-paper": { width: { xs: "100%", sm: 480 }, p: 3 },
            }}
          >
            <Stack
              direction="row"
              justifyContent="space-between"
              alignItems="center"
              mb={3}
            >
              <Typography variant="h5">Фоновые операции</Typography>
              <IconButton
                aria-label="Закрыть операции"
                onClick={() => setActivity(false)}
              >
                <CloseRounded />
              </IconButton>
            </Stack>
            <Stack spacing={2}>
              {operations.length === 0 ? (
                <Empty
                  title="Операций пока нет"
                  text="Здесь появятся обновления подписок, тесты и применения конфигурации."
                />
              ) : (
                operations.map((o) => (
                  <Card key={o.id}>
                    <CardContent>
                      <Stack
                        direction="row"
                        justifyContent="space-between"
                        alignItems="center"
                      >
                        <Typography fontWeight={650}>
                          {o.data.action}
                        </Typography>
                        <Chip
                          size="small"
                          label={
                            {
                              queued: "В очереди",
                              running: "Выполняется",
                              succeeded: "Готово",
                              failed: "Ошибка",
                              cancelled: "Отменена",
                            }[o.data.status as string] || o.data.status
                          }
                          color={
                            o.data.status === "succeeded"
                              ? "success"
                              : o.data.status === "failed"
                                ? "error"
                                : "default"
                          }
                        />
                      </Stack>
                      <Typography variant="caption" color="text.secondary">
                        {ago(o.updated_at)}
                      </Typography>
                      {["queued", "running"].includes(o.data.status) && (
                        <LinearProgress sx={{ mt: 2 }} />
                      )}
                      {o.data.error && (
                        <Alert severity="error" sx={{ mt: 2 }}>
                          {message(new Error(o.data.error))}
                        </Alert>
                      )}
                      {o.data.result && (
                        <Box mt={2}>
                          {o.data.result.items || o.data.result.preview ? (
                            <Button
                              onClick={() =>
                                setPreview(
                                  o.data.result.preview || o.data.result,
                                )
                              }
                            >
                              Посмотреть результат
                            </Button>
                          ) : (
                            <Typography variant="body2" color="text.secondary">
                              {o.data.result.mbps
                                ? `${Number(o.data.result.mbps).toFixed(1)} Мбит/с · ${Number(o.data.result.ttfb_ms).toFixed(1)} мс`
                                : o.data.result.healthy
                                  ? `Проверка пройдена · ${Number(o.data.result.ttfb_ms).toFixed(1)} мс`
                                  : o.data.result.status ||
                                    "Операция завершена"}
                            </Typography>
                          )}
                        </Box>
                      )}
                      {o.data.status === "queued" && (
                        <Button
                          size="small"
                          onClick={() =>
                            api(`operations/${o.id}/cancel`, "POST", {})
                              .then(() => load(true))
                              .catch((e) => notify(message(e), "error"))
                          }
                        >
                          Отменить
                        </Button>
                      )}
                    </CardContent>
                  </Card>
                ))
              )}
            </Stack>
          </Drawer>
        </Box>
      )}
      <Snackbar
        open={!!toast}
        autoHideDuration={6000}
        onClose={() => setToast(null)}
        anchorOrigin={{ vertical: "bottom", horizontal: "center" }}
      >
        <Alert
          severity={toast?.severity || "info"}
          onClose={() => setToast(null)}
          variant="filled"
          sx={{ boxShadow: 4 }}
        >
          {toast?.text}
        </Alert>
      </Snackbar>
    </ThemeProvider>
  );
}
function Empty({
  title,
  text,
  action,
}: {
  title: string;
  text: string;
  action?: React.ReactNode;
}) {
  return (
    <Box
      sx={{
        py: 6,
        px: 3,
        textAlign: "center",
        border: 1,
        borderColor: "divider",
        borderStyle: "dashed",
        borderRadius: 5,
      }}
    >
      <Box
        sx={{
          display: "inline-flex",
          p: 2,
          bgcolor: "action.selected",
          borderRadius: 4,
          mb: 2,
          color: "primary.main",
        }}
      >
        <HubRounded sx={{ fontSize: 32 }} />
      </Box>
      <Typography variant="h6">{title}</Typography>
      <Typography
        color="text.secondary"
        variant="body2"
        sx={{ mt: 1, mb: 3, maxWidth: 420, mx: "auto" }}
      >
        {text}
      </Typography>
      {action}
    </Box>
  );
}
function CoreInstall({ install }: { install: (v: string) => void }) {
  const [version, setVersion] = useState("");
  return (
    <Paper variant="outlined" sx={{ p: 3, borderRadius: 4 }}>
      <Stack
        direction={{ xs: "column", sm: "row" }}
        gap={2}
        alignItems={{ sm: "center" }}
      >
        <TextField
          label="Версия Xray"
          value={version}
          onChange={(e) => setVersion(e.target.value)}
          placeholder="Номер версии релиза"
        />
        <Button
          variant="contained"
          startIcon={<CloudDownloadRounded />}
          onClick={() => install(version)}
          disabled={!/^\d{1,2}\.\d{1,2}\.\d{1,2}$/.test(version)}
        >
          Загрузить и проверить
        </Button>
        <Typography variant="caption" color="text.secondary">
          Загрузка выполняется в фоне. Работающие соединения сохраняются.
        </Typography>
      </Stack>
    </Paper>
  );
}
function EventList({ items }: { items: any[] }) {
  return (
    <Card>
      <CardContent>
        <Stack divider={<Divider />}>
          {[...items].reverse().map((e) => (
            <Stack key={e.id} direction="row" gap={2} py={2} alignItems="start">
              <Avatar
                sx={{
                  width: 32,
                  height: 32,
                  bgcolor: "action.selected",
                  color: e.action === "failed" ? "error.main" : "primary.main",
                }}
              >
                {e.action === "failed" ? (
                  <WarningAmberRounded sx={{ fontSize: 18 }} />
                ) : (
                  <CheckCircleOutlineRounded sx={{ fontSize: 18 }} />
                )}
              </Avatar>
              <Box sx={{ flex: 1, minWidth: 0 }}>
                <Typography variant="body2" fontWeight={650}>
                  {e.kind} · {e.action}
                </Typography>
                <Typography
                  variant="caption"
                  color="text.secondary"
                  sx={{ overflowWrap: "anywhere" }}
                >
                  {e.detail?.error
                    ? message(new Error(e.detail.error))
                    : e.detail?.name || e.detail?.action || e.object_id}
                </Typography>
              </Box>
              <Typography
                variant="caption"
                color="text.secondary"
                sx={{ whiteSpace: "nowrap" }}
              >
                {new Date(e.time).toLocaleString()}
              </Typography>
            </Stack>
          ))}
        </Stack>
        {items.length === 0 && (
          <Empty
            title="Журнал пуст"
            text="Изменения конфигурации будут появляться здесь."
          />
        )}
      </CardContent>
    </Card>
  );
}
function Settings({
  item,
  other,
  save,
  notify,
  operate,
  refresh,
  confirm,
}: {
  item: Item;
  other: any;
  save: (data: any) => Promise<void>;
  notify: (text: string, severity?: Toast["severity"]) => void;
  operate: (path: string, body?: unknown) => void;
  refresh: () => void;
  confirm: (v: {
    title: string;
    text: string;
    action: () => Promise<void>;
  }) => void;
}) {
  const [data, setData] = useState(item.data);
  const [busy, setBusy] = useState(false);
  const [password, setPassword] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [backupPlan, setBackupPlan] = useState<any>(null);
  const [tab, setTab] = useState(0);
  useEffect(() => setData(item.data), [item.version]);
  const set = (k: string, v: any) => setData((d) => ({ ...d, [k]: v }));
  const upload = () => {
    const form = new FormData();
    if (file) form.append("file", file);
    form.append("password", password);
    return form;
  };
  return (
    <Stack spacing={3}>
      <Tabs value={tab} onChange={(_, v) => setTab(v)} variant="scrollable">
        <Tab label="Сеть и автономность" />
        <Tab label="Резервные копии" />
        <Tab label="Ревизии и корзина" />
      </Tabs>
      {tab === 0 && (
        <Card>
          <CardContent>
            <Box
              component="form"
              onSubmit={async (e) => {
                e.preventDefault();
                setBusy(true);
                try {
                  await save(data);
                } catch (e) {
                  notify(message(e), "error");
                } finally {
                  setBusy(false);
                }
              }}
            >
              <Stack spacing={3}>
                <Typography variant="h6">Параметры воркера</Typography>
                <Typography variant="h6">Каналы ISP</Typography>
                <Alert severity="info">
                  Каждый путь - локальный исходящий IP и отдельный ISP. Адрес
                  должен существовать в сети воркера, а роутер должен строго
                  направлять его через нужный канал. Сохранение настроек не
                  меняет сеть и не включает WAN. После сохранения примените
                  конфигурацию.
                </Alert>
                {(data.egress_paths || []).map((path: any, index: number) => {
                  const update = (key: string, value: any) =>
                    set(
                      "egress_paths",
                      data.egress_paths.map((p: any, i: number) =>
                        i === index ? { ...p, [key]: value } : p,
                      ),
                    );
                  return (
                    <Box
                      key={index}
                      sx={{
                        border: 1,
                        borderColor: "divider",
                        borderRadius: 2,
                        p: 2,
                      }}
                    >
                      <Stack spacing={2}>
                        <FormControlLabel
                          control={
                            <Switch
                              checked={!!path.enabled}
                              onChange={(_, value) => update("enabled", value)}
                            />
                          }
                          label={
                            path.enabled
                              ? "Доступен для выбора"
                              : "Отключён - без соединений и проверок"
                          }
                        />
                        <Box className="two-col">
                          <TextField
                            label="Идентификатор ISP"
                            value={path.id}
                            onChange={(e) => update("id", e.target.value)}
                            required
                          />
                          <TextField
                            label="Название"
                            value={path.name}
                            onChange={(e) => update("name", e.target.value)}
                          />
                          <TextField
                            label="Исходящий IP (sendThrough)"
                            value={path.source_ip}
                            onChange={(e) =>
                              update("source_ip", e.target.value)
                            }
                            required
                          />
                          <TextField
                            label="Вес новых соединений"
                            type="number"
                            value={path.weight}
                            onChange={(e) =>
                              update("weight", Number(e.target.value))
                            }
                            slotProps={{ htmlInput: { min: 1, max: 1000 } }}
                          />
                          <TextField
                            label="Загрузка ISP, Мбит/с"
                            type="number"
                            value={path.download_mbps}
                            onChange={(e) =>
                              update("download_mbps", Number(e.target.value))
                            }
                          />
                          <TextField
                            label="Отдача ISP, Мбит/с"
                            type="number"
                            value={path.upload_mbps}
                            onChange={(e) =>
                              update("upload_mbps", Number(e.target.value))
                            }
                          />
                        </Box>
                        <Button
                          color="error"
                          sx={{ alignSelf: "start" }}
                          onClick={() =>
                            set(
                              "egress_paths",
                              data.egress_paths.filter(
                                (_: any, i: number) => i !== index,
                              ),
                            )
                          }
                        >
                          Удалить путь {path.id}
                        </Button>
                      </Stack>
                    </Box>
                  );
                })}
                <Button
                  sx={{ alignSelf: "start" }}
                  disabled={(data.egress_paths || []).length >= 4}
                  onClick={() =>
                    set("egress_paths", [
                      ...(data.egress_paths || []),
                      {
                        id: "isp" + ((data.egress_paths || []).length + 1),
                        name: "",
                        source_ip: "",
                        enabled: false,
                        weight: 1,
                        download_mbps: 100,
                        upload_mbps: 100,
                      },
                    ])
                  }
                >
                  Добавить канал ISP
                </Button>
                <Box className="two-col">
                  <TextField
                    label="Xray по умолчанию"
                    value={data.default_core}
                    onChange={(e) => set("default_core", e.target.value)}
                  />
                  <TextField
                    label="Адрес сервера для UDP (или auto в host-режиме)"
                    value={data.udp_advertise}
                    onChange={(e) => set("udp_advertise", e.target.value)}
                    helperText="LAN/NetBird IP. Переменная PROXY_ADVERTISE_IP имеет приоритет."
                  />
                  <TextField
                    label="Начало UDP-диапазона"
                    type="number"
                    value={data.udp_port_min}
                    onChange={(e) =>
                      set("udp_port_min", Number(e.target.value))
                    }
                  />
                  <TextField
                    label="Конец UDP-диапазона"
                    type="number"
                    value={data.udp_port_max}
                    onChange={(e) =>
                      set("udp_port_max", Number(e.target.value))
                    }
                  />
                  <TextField
                    label="Автономная аренда, ч"
                    type="number"
                    value={data.offline_lease_seconds / 3600}
                    onChange={(e) =>
                      set(
                        "offline_lease_seconds",
                        Number(e.target.value) * 3600,
                      )
                    }
                  />
                  <TextField
                    label="Адрес привязки прокси"
                    value={data.proxy_bind}
                    onChange={(e) => set("proxy_bind", e.target.value)}
                  />
                  <TextField
                    label="Канал загрузки, Мбит/с"
                    type="number"
                    value={data.download_mbps}
                    onChange={(e) =>
                      set("download_mbps", Number(e.target.value))
                    }
                  />
                  <TextField
                    label="Канал отдачи, Мбит/с"
                    type="number"
                    value={data.upload_mbps}
                    onChange={(e) => set("upload_mbps", Number(e.target.value))}
                  />
                </Box>
                <Alert severity="info">
                  Диапазон 31000-31255 опубликован в Compose. Изменение
                  диапазона требует согласованного изменения Compose. После
                  потери управления воркер продолжает работу до конца аренды;
                  срок годности подписок проверяется отдельно.
                </Alert>
                <Typography color="text.secondary" variant="body2">
                  Пароль администратора задаётся через ADMIN_PASSWORD. После
                  изменения env пересоздайте control-plane; существующие сессии
                  будут аннулированы. Для HTTPS установите COOKIE_SECURE=true.
                </Typography>
                <Button
                  type="submit"
                  variant="contained"
                  loading={busy}
                  sx={{ alignSelf: "start" }}
                >
                  Сохранить настройки
                </Button>
              </Stack>
            </Box>
          </CardContent>
        </Card>
      )}
      {tab === 1 && (
        <Card>
          <CardContent>
            <Stack spacing={3}>
              <Typography variant="h6">
                Зашифрованная резервная копия
              </Typography>
              <Typography color="text.secondary" variant="body2">
                Копия содержит конфигурацию, нормализованный каталог и
                сертификаты управляющего сервиса. По умолчанию используется
                пароль администратора. Храните копию и её пароль раздельно в
                надёжных местах.
              </Typography>
              <TextField
                label="Пароль резервной копии"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                helperText="Необязательно. Не передавайте копию без шифрования."
                autoComplete="new-password"
              />
              <Button
                variant="contained"
                startIcon={<CloudDownloadRounded />}
                sx={{ alignSelf: "start" }}
                onClick={async () => {
                  try {
                    await downloadBackup(password);
                    notify("Резервная копия скачана");
                  } catch (e) {
                    notify(message(e), "error");
                  }
                }}
              >
                Скачать копию
              </Button>
              <Divider />
              <Typography variant="h6">Восстановить настройки</Typography>
              <Button
                variant="outlined"
                component="label"
                sx={{ alignSelf: "start" }}
              >
                {file ? file.name : "Выбрать файл .rwb"}
                <input
                  type="file"
                  hidden
                  accept=".rwb"
                  onChange={(e) => {
                    setFile(e.target.files?.[0] || null);
                    setBackupPlan(null);
                  }}
                />
              </Button>
              <Button
                disabled={!file}
                sx={{ alignSelf: "start" }}
                onClick={async () => {
                  try {
                    setBackupPlan(
                      await api("backup/preview", "POST", upload()),
                    );
                  } catch (e) {
                    notify(message(e), "error");
                  }
                }}
              >
                Проверить копию
              </Button>
              {backupPlan && (
                <>
                  <Alert severity="success">
                    Копия проверена ·{" "}
                    {new Date(backupPlan.created_at).toLocaleString()}
                    <br />
                    {Object.entries(backupPlan.counts)
                      .map(
                        ([k, v]) =>
                          `${({ sources: "Подписки", aggregates: "Агрегаты", pools: "Пулы", profiles: "Профили", policies: "Политики", listeners: "Точки входа", settings: "Настройки", "core-artifacts": "Сборки Xray" } as Record<string, string>)[k] || k}: ${v}`,
                      )
                      .join(" · ")}
                  </Alert>
                  <Button
                    color="warning"
                    variant="contained"
                    sx={{ alignSelf: "start" }}
                    onClick={() =>
                      confirm({
                        title: "Заменить настройки из резервной копии?",
                        text: "Текущие настройки и каталог будут заменены. Рабочая ревизия и идентификаторы этого развёртывания сохранятся до явного применения восстановленной конфигурации.",
                        action: async () => {
                          await api("backup/restore", "POST", upload());
                          refresh();
                          notify(
                            "Настройки восстановлены. Проверьте и примените конфигурацию.",
                          );
                          setBackupPlan(null);
                        },
                      })
                    }
                  >
                    Восстановить настройки
                  </Button>
                </>
              )}
            </Stack>
          </CardContent>
        </Card>
      )}
      {tab === 2 && (
        <Stack spacing={3}>
          <Card>
            <CardContent>
              <Typography variant="h6" mb={2}>
                Ревизии конфигурации
              </Typography>
              <Stack spacing={1.5}>
                {[...(other.revisions || [])]
                  .sort((a: any, b: any) => b.generation - a.generation)
                  .map((v: any) => (
                    <Stack
                      key={v.id}
                      direction="row"
                      alignItems="center"
                      justifyContent="space-between"
                      gap={2}
                    >
                      <Box>
                        <Typography variant="body2" fontWeight={650}>
                          Ревизия {v.generation} · {v.status}
                        </Typography>
                        <Typography variant="caption" color="text.secondary">
                          {new Date(v.created_at).toLocaleString()} ·{" "}
                          {countRu(v.routes, "профиль", "профиля", "профилей")}
                        </Typography>
                      </Box>
                      <Button
                        size="small"
                        onClick={() =>
                          confirm({
                            title: "Откатить конфигурацию?",
                            text: `Будет создана новая ревизия по структуре ревизии ${v.generation}. Каталог подписок проверяется заново, просроченные данные не восстанавливаются в работу.`,
                            action: async () => {
                              operate("config/rollback", { revision_id: v.id });
                            },
                          })
                        }
                      >
                        Откатить
                      </Button>
                    </Stack>
                  ))}
              </Stack>
            </CardContent>
          </Card>
          <Card>
            <CardContent>
              <Typography variant="h6" mb={2}>
                Корзина · 24 часа
              </Typography>
              <Stack spacing={1.5}>
                {other.trash?.length === 0 && (
                  <Typography color="text.secondary" variant="body2">
                    Корзина пуста.
                  </Typography>
                )}
                {other.trash?.map((v: any) => (
                  <Stack
                    key={v.id}
                    direction="row"
                    alignItems="center"
                    justifyContent="space-between"
                  >
                    <Typography variant="body2">
                      {v.name} · {labels[v.kind] || v.kind}
                    </Typography>
                    <Button
                      onClick={async () => {
                        try {
                          await api(`trash/${v.id}/restore`, "POST", {});
                          refresh();
                          notify("Объект восстановлен");
                        } catch (e) {
                          notify(message(e), "error");
                        }
                      }}
                    >
                      Восстановить
                    </Button>
                  </Stack>
                ))}
              </Stack>
            </CardContent>
          </Card>
        </Stack>
      )}
    </Stack>
  );
}
function Help() {
  return (
    <Stack spacing={3}>
      <Card>
        <CardContent>
          <Typography variant="h6" mb={2}>
            Как подключить клиент
          </Typography>
          <Typography color="text.secondary" paragraph>
            Создайте подписку, агрегат или пул, затем политику и точку входа.
            Нажмите «Применить» и дождитесь успешного результата в фоновых
            операциях.
          </Typography>
          <Box className="two-col">
            <Paper variant="outlined" sx={{ p: 2.5, borderRadius: 3 }}>
              <Typography fontWeight={650}>
                HTTP / HTTPS через CONNECT
              </Typography>
              <Typography fontFamily="monospace" variant="body2" mt={1}>
                http://SERVER_IP:18080
              </Typography>
              <Typography variant="caption" color="text.secondary">
                Bind и внешний порт: 18080
              </Typography>
            </Paper>
            <Paper variant="outlined" sx={{ p: 2.5, borderRadius: 3 }}>
              <Typography fontWeight={650}>SOCKS5 TCP / UDP</Typography>
              <Typography fontFamily="monospace" variant="body2" mt={1}>
                socks5h://SERVER_IP:11080
              </Typography>
              <Typography variant="caption" color="text.secondary">
                Bind и внешний порт: 11080
              </Typography>
            </Paper>
          </Box>
          <Typography variant="body2" color="text.secondary" mt={2}>
            Для входов с аутентификацией предусмотрены отдельные порты 18081 и
            11081. Создайте соответствующие точки входа на 18081 и 11081. Доступ
            ограничивается адресом BIND_IP, правилами LAN/NetBird и CIDR точки
            входа.
          </Typography>
        </CardContent>
      </Card>
      <Card>
        <CardContent>
          <Typography variant="h6" mb={2}>
            Что важно для скорости и стабильности
          </Typography>
          <Stack spacing={2}>
            {[
              "Балансировка распределяет соединения. Скорость одного TCP-потока ограничена выбранным сервером и каналом.",
              "Все режимы сохраняют открытые сеансы на исходном маршруте. UDP-путь закреплён за ассоциацией до её закрытия.",
              "Задержка и скорость - разные величины. Приложение показывает источник замера, время и ограниченный тестовый объём.",
              "Текущий адаптер Xray передаёт SOCKS5 UDP-кадр до 7168 байт. FRAG не поддерживается. Пустые UDP-пакеты ограничены поведением Xray.",
              "Доступность воркера и доступность маршрутов проверяются отдельно. Потеря провайдера не вызывает бесполезный перезапуск всего воркера.",
              "При отсутствии здоровых путей трафик не отправляется напрямую. Истёкшая аренда блокирует новые соединения, сохраняя открытые сеансы.",
            ].map((text) => (
              <Stack key={text} direction="row" gap={1.5}>
                <InfoOutlined color="primary" sx={{ fontSize: 20, mt: 0.2 }} />
                <Typography variant="body2" color="text.secondary">
                  {text}
                </Typography>
              </Stack>
            ))}
          </Stack>
        </CardContent>
      </Card>
      <Card>
        <CardContent>
          <Typography variant="h6" mb={2}>
            API и наблюдаемость
          </Typography>
          <Typography variant="body2" color="text.secondary" paragraph>
            API доступен по /api/v1. Вход POST /auth/login принимает только
            password. Сессионная cookie используется вместе с X-CSRF-Token для
            изменений. PATCH и DELETE требуют числовой If-Match. Фоновые
            операции возвращают 202 и идентификатор для /operations/ID.
          </Typography>
          <Stack direction="row" spacing={2} flexWrap="wrap">
            <Button
              href="/api/v1/openapi.json"
              target="_blank"
              startIcon={<DescriptionRounded />}
            >
              OpenAPI
            </Button>
            {grafanaURL && (
              <Button
                href={grafanaURL}
                target="_blank"
                rel="noopener noreferrer"
                startIcon={<BarChartRounded />}
              >
                Grafana
              </Button>
            )}
          </Stack>
          <Typography
            variant="caption"
            color="text.secondary"
            display="block"
            mt={2}
          >
            Метрики и логи доступны во внешней Grafana с её собственной
            авторизацией. Prometheus и Loki запускаются профилем observability.
            Метрики не публикуются наружу.
          </Typography>
        </CardContent>
      </Card>
    </Stack>
  );
}
createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
