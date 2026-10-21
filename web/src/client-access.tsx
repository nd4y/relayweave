import { useEffect, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Checkbox,
  FormControlLabel,
  MenuItem,
  Paper,
  Snackbar,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import { api, message } from "./api";
type Listener = {
  id: string;
  name: string;
  protocol: string;
  port: number;
  username: string;
  authentication: boolean;
  hosts: string[];
};
type Grant = { id: string; listener: string; host: string; expires: string };
type Metadata = {
  hosts: { name: string; host: string; http_only?: boolean; port?: number }[];
  listeners: Listener[];
  subscriptions: Grant[];
};
type Exported = {
  uri: string;
  throne_uri: string;
  config: string;
  contains_credentials: boolean;
};
const base64 = (s: string) =>
  btoa(
    Array.from(new TextEncoder().encode(s), (v) => String.fromCharCode(v)).join(
      "",
    ),
  );
export function ClientAccess() {
  const [meta, setMeta] = useState<Metadata | null>(null),
    [listener, setListener] = useState(""),
    [host, setHost] = useState(""),
    [password, setPassword] = useState(""),
    [consent, setConsent] = useState(false),
    [reveal, setReveal] = useState(false),
    [result, setResult] = useState<Exported | null>(null),
    [subscription, setSubscription] = useState(""),
    [hours, setHours] = useState(720),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [feedback, setFeedback] = useState("");
  const selectedHost = meta?.hosts.find((h) => h.host === host);
  const selected = meta?.listeners.find((l) => l.id === listener);
  const load = async () => {
    const data = await api<Metadata>("client-access");
    setMeta(data);
    setListener((v) =>
      data.listeners.some((l) => l.id === v)
        ? v
        : data.listeners.find((l) =>
            l.hosts.includes(data.hosts[0]?.host || ""),
          )?.id || "",
    );
    setHost((v) =>
      data.hosts.some((h) => h.host === v) ? v : data.hosts[0]?.host || "",
    );
  };
  useEffect(() => {
    let live = true;
    api<Metadata>("client-access")
      .then((data) => {
        if (live) {
          setMeta(data);
          setListener(
            data.listeners.find((l) =>
              l.hosts.includes(data.hosts[0]?.host || ""),
            )?.id || "",
          );
          setHost(data.hosts[0]?.host || "");
        }
      })
      .catch((e) => {
        if (live) setError(message(e));
      });
    return () => {
      live = false;
    };
  }, []);
  const clear = () => {
    setResult(null);
    setSubscription("");
    setReveal(false);
  };
  const copy = async (value: string) => {
    try {
      if (!navigator.clipboard?.writeText) throw Error("clipboard");
      await navigator.clipboard.writeText(value);
      setFeedback("Скопировано");
    } catch {
      setFeedback(
        "Буфер недоступен в этом браузере. Скачайте файл или откройте страницу по HTTPS.",
      );
    }
  };
  const download = (value: string, name: string, type = "application/json") => {
    const url = URL.createObjectURL(new Blob([value], { type }));
    const a = document.createElement("a");
    a.href = url;
    a.download = name;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    setFeedback("Файл подготовлен к скачиванию");
  };
  const generate = async (action: "render" | "subscriptions") => {
    setBusy(true);
    setError("");
    try {
      const value = await api("client-access/" + action, "POST", {
        listener,
        host,
        password,
        hours,
        confirm_secrets: consent,
      });
      if (action === "render") {
        setResult(value);
        setFeedback("Экспорт подготовлен");
      } else {
        setSubscription(new URL(value.path, window.location.origin).href);
        setFeedback("Подписка создана. Ссылка доступна только сейчас.");
        await load();
      }
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const revoke = async (id: string) => {
    setBusy(true);
    try {
      await api("client-access/" + id, "DELETE");
      setSubscription("");
      await load();
      setFeedback("Подписка отозвана");
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Stack spacing={2} data-testid="client-access">
      <Typography variant="h5">Подключить клиент к RelayWeave</Typography>
      <Typography color="text.secondary">
        Выберите доступный адрес и вход. Экспорт содержит подключение к
        RelayWeave. Подписки провайдеров и их ключи остаются на сервере.
      </Typography>
      {error && <Alert severity="error">{error}</Alert>}
      {!meta ? (
        <Typography>Загрузка подключений...</Typography>
      ) : !meta.listeners.length ? (
        <Alert severity="info">
          Нет опубликованных включённых HTTP/SOCKS5 входов. Проверьте deployment
          и точки входа.
        </Alert>
      ) : (
        <Paper variant="outlined" sx={{ p: 2 }}>
          <Stack spacing={2}>
            <Stack direction={{ xs: "column", sm: "row" }} spacing={2}>
              <TextField
                select
                label="Адрес подключения"
                value={host}
                onChange={(e) => {
                  setHost(e.target.value);
                  clear();
                }}
                fullWidth
              >
                {meta.hosts.map((h) => (
                  <MenuItem key={h.name + h.host} value={h.host}>
                    {h.name} · {h.host}
                  </MenuItem>
                ))}
              </TextField>
              <TextField
                select
                label="Вход RelayWeave"
                value={listener}
                onChange={(e) => {
                  setListener(e.target.value);
                  setPassword("");
                  setConsent(false);
                  clear();
                }}
                fullWidth
              >
                {meta.listeners
                  .filter((l) => l.hosts.includes(host))
                  .map((l) => (
                    <MenuItem key={l.id} value={l.id}>
                      {l.name} ·{" "}
                      {l.protocol === "socks5" ? "SOCKS5" : "HTTP CONNECT"} ·{" "}
                      {l.port}
                    </MenuItem>
                  ))}
              </TextField>
            </Stack>
            {selected && (
              <Alert severity={selected.authentication ? "info" : "warning"}>
                {selected.protocol === "socks5"
                  ? "SOCKS5 TCP/UDP"
                  : "HTTP CONNECT/TCP"}{" "}
                · порт {selectedHost?.port || selected.port}.{" "}
                {selected.authentication
                  ? "Доступ с именем и паролем."
                  : "На этом действующем входе пароль не настроен. Используйте только доверенную сеть."}{" "}
                Доступ по NetBird требует доступного маршрута к выбранному
                адресу.
              </Alert>
            )}
            {selected?.authentication && (
              <>
                <TextField
                  label="Имя пользователя"
                  value={selected.username}
                  slotProps={{ input: { readOnly: true } }}
                />
                <TextField
                  label="Действующий пароль этого входа"
                  type={reveal ? "text" : "password"}
                  value={password}
                  autoComplete="off"
                  onChange={(e) => {
                    setPassword(e.target.value);
                    clear();
                  }}
                  helperText="Введите пароль для включения в клиентскую конфигурацию. Пароль входа не изменится."
                />
              </>
            )}
            <FormControlLabel
              control={
                <Checkbox
                  checked={consent}
                  onChange={(e) => setConsent(e.target.checked)}
                />
              }
              label="Разрешаю включить данные доступа в экспорт и понимаю, что ссылка подписки предоставляет доступ к этой конфигурации"
            />
            <Stack direction="row" gap={1} flexWrap="wrap">
              <Button
                variant="contained"
                disabled={
                  busy ||
                  !host ||
                  !listener ||
                  (!!selected?.authentication && (!password || !consent))
                }
                onClick={() => void generate("render")}
              >
                Подготовить конфиг
              </Button>
              <Button
                disabled={
                  busy ||
                  !host ||
                  !listener ||
                  !consent ||
                  (!!selected?.authentication && !password)
                }
                onClick={() => void generate("subscriptions")}
              >
                Создать ссылку подписки
              </Button>
              <TextField
                size="small"
                type="number"
                label="Срок ссылки, часов"
                value={hours}
                inputProps={{ min: 1, max: 2160 }}
                onChange={(e) => setHours(Number(e.target.value))}
              />
            </Stack>
            {(result || subscription || password) && (
              <FormControlLabel
                control={
                  <Checkbox
                    checked={reveal}
                    onChange={(e) => setReveal(e.target.checked)}
                  />
                }
                label="Показать данные доступа на экране"
              />
            )}
            {result && (
              <Box>
                <Stack direction="row" gap={1} flexWrap="wrap">
                  <Button onClick={() => void copy(result.throne_uri)}>
                    Копировать для Throne
                  </Button>
                  <Button onClick={() => void copy(result.uri)}>
                    Копировать URI{" "}
                    {selected?.protocol === "socks5" ? "SOCKS5" : "HTTP"}
                  </Button>
                  <Button onClick={() => void copy(result.config)}>
                    Копировать JSON
                  </Button>
                  <Button
                    onClick={() =>
                      download(result.config, "relayweave-client.json")
                    }
                  >
                    Скачать конфиг
                  </Button>
                </Stack>
                <TextField
                  label="Конфигурация подключения"
                  multiline
                  minRows={3}
                  maxRows={10}
                  value={
                    reveal
                      ? result.config
                      : "Данные скрыты. Используйте кнопку копирования или скачивания."
                  }
                  fullWidth
                  slotProps={{ input: { readOnly: true } }}
                />
              </Box>
            )}
            {subscription && (
              <Alert severity="info">
                <Stack spacing={1}>
                  <Typography>
                    Ссылка подписки не требует admin cookie. Храните её как
                    пароль. Ссылку можно отозвать ниже.
                  </Typography>
                  <TextField
                    label="Ссылка подписки"
                    value={reveal ? subscription : "••••••••"}
                    fullWidth
                    slotProps={{ input: { readOnly: true } }}
                  />
                  <Stack direction="row" gap={1} flexWrap="wrap">
                    <Button onClick={() => void copy(subscription)}>
                      Копировать ссылку подписки
                    </Button>
                    <Button
                      onClick={() =>
                        void copy(
                          "throne://addsub/" +
                            base64(subscription + "#RelayWeave"),
                        )
                      }
                    >
                      Throne: добавить подписку
                    </Button>
                    <Button
                      onClick={() =>
                        download(
                          subscription,
                          "relayweave-subscription.txt",
                          "text/plain",
                        )
                      }
                    >
                      Скачать ссылку
                    </Button>
                  </Stack>
                  {window.location.protocol !== "https:" && (
                    <Typography variant="body2">
                      Сейчас используется LAN HTTP. Для передачи ссылки через
                      внешнюю сеть откройте эту страницу по настроенному HTTPS
                      адресу или через защищённый VPN.
                    </Typography>
                  )}
                </Stack>
              </Alert>
            )}
          </Stack>
        </Paper>
      )}
      {!!meta?.subscriptions.length && (
        <Paper variant="outlined" sx={{ p: 2 }}>
          <Typography variant="h6">Ссылки подписки</Typography>
          {meta.subscriptions.map((g) => (
            <Stack
              key={g.id}
              direction="row"
              alignItems="center"
              justifyContent="space-between"
              gap={2}
            >
              <Typography>
                {g.host} · до {new Date(g.expires).toLocaleString()}
              </Typography>
              <Button
                disabled={busy}
                color="error"
                onClick={() => void revoke(g.id)}
              >
                Отозвать
              </Button>
            </Stack>
          ))}
        </Paper>
      )}
      <Typography variant="body2" color="text.secondary">
        В Throne вставьте ссылку из кнопки «Копировать для Throne» или
        импортируйте JSON. Обычная HTTP URI в Throne открывает выбор типа
        импорта. Подписка возвращает sing-box JSON с outbound HTTP/SOCKS5.
        Полная настройка TUN и маршрутизации остаётся в клиенте.
      </Typography>
      <Snackbar
        key={feedback}
        open={!!feedback}
        autoHideDuration={6000}
        onClose={(_,reason) => {if(reason!=="clickaway")setFeedback("")}}
        message={feedback}
      />
    </Stack>
  );
}
