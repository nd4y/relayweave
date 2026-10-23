import { useEffect, useRef, useState } from "react";
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Alert,
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  MenuItem,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import ExpandMoreRounded from "@mui/icons-material/ExpandMoreRounded";
import DeleteOutlineRounded from "@mui/icons-material/DeleteOutlineRounded";
import { api, message, type Item } from "./api";

type Entry = {
  selector: string;
  bytes: number;
  protected: boolean;
  reasons: string[];
};
type Cache = {
  items: Entry[];
  bytes: number;
  free_bytes: number;
  max_entries: number;
  reserve_bytes: number;
};
const mib = (bytes: number) =>
  (bytes / 1048576).toLocaleString("ru-RU", { maximumFractionDigits: 1 }) +
  " МиБ";
const reasons: Record<string, string> = {
  current: "Текущая конфигурация",
  constraints: "Ограничения конфигурации",
  running_or_draining: "Работающие или завершающиеся соединения",
  previous_good: "Резервная конфигурация",
  previous_constraints: "Резервные ограничения",
  builtin: "Включена в образ",
};
export function CoreCache() {
  const [workers, setWorkers] = useState<{ id: string; online: boolean }[]>([]);
  const [worker, setWorker] = useState("worker-1");
  const [cache, setCache] = useState<Cache>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [remove, setRemove] = useState<Entry>();
  const active = useRef(true);
  useEffect(() => {
    active.current = true;
    api("workers")
      .then((r) => {
        if (active.current) setWorkers(r.items || []);
      })
      .catch((e) => {
        if (active.current) setError(message(e));
      });
    return () => {
      active.current = false;
    };
  }, []);
  const run = async (action: "cache" | "remove", version?: string) => {
    setBusy(true);
    setError("");
    setRemove(undefined);
    try {
      const operation = await api<Item>("cores/" + action, "POST", {
        worker_id: worker,
        version,
      });
      const deadline = Date.now() + 150000;
      while (active.current && Date.now() < deadline) {
        const current = await api<Item>("operations/" + operation.id);
        if (current.data.status === "succeeded") {
          setCache(current.data.result);
          return;
        }
        if (["failed", "cancelled"].includes(current.data.status))
          throw new Error(current.data.error || "operation_failed");
        await new Promise((resolve) => setTimeout(resolve, 400));
      }
      if (active.current) throw new Error("request_timeout");
    } catch (e) {
      if (active.current) setError(message(e));
    } finally {
      if (active.current) setBusy(false);
    }
  };
  return (
    <Accordion disableGutters sx={{ borderRadius: 4, overflow: "hidden" }}>
      <AccordionSummary expandIcon={<ExpandMoreRounded />}>
        <Typography fontWeight={650}>Кэш ядер на воркерах</Typography>
      </AccordionSummary>
      <AccordionDetails>
        <Stack spacing={2}>
          <Typography color="text.secondary">
            Удаление освобождает место только на выбранном воркере. Активные
            версии, незавершённые соединения и конфигурации восстановления
            защищены. Одобрение сборки сохраняется, поэтому она может быть
            загружена повторно.
          </Typography>
          <Stack direction={{ xs: "column", sm: "row" }} spacing={2}>
            <TextField
              select
              label="Воркер"
              value={worker}
              disabled={busy}
              onChange={(e) => {
                setWorker(e.target.value);
                setCache(undefined);
              }}
              sx={{ minWidth: 200 }}
            >
              {(workers.length
                ? workers
                : [{ id: "worker-1", online: true }]
              ).map((w) => (
                <MenuItem key={w.id} value={w.id} disabled={!w.online}>
                  {w.id}
                  {w.online ? "" : " - недоступен"}
                </MenuItem>
              ))}
            </TextField>
            <Button
              variant="outlined"
              loading={busy}
              onClick={() => void run("cache")}
            >
              Обновить кэш
            </Button>
          </Stack>
          {error && (
            <Alert severity="error" role="alert">
              {error}
            </Alert>
          )}
          {cache && (
            <>
              <Typography variant="body2">
                Загружено {cache.items.length} из {cache.max_entries} сборок,
                занято {mib(cache.bytes)}. Свободно {mib(cache.free_bytes)},
                резерв для установки {mib(cache.reserve_bytes)}.
              </Typography>
              {!cache.items.length && (
                <Alert severity="info">
                  Дополнительных сборок нет. Основное ядро находится в образе.
                </Alert>
              )}
              {cache.items.map((entry) => (
                <Box
                  key={entry.selector}
                  sx={{
                    p: 2,
                    border: 1,
                    borderColor: "divider",
                    borderRadius: 3,
                  }}
                >
                  <Stack
                    direction={{ xs: "column", sm: "row" }}
                    spacing={2}
                    alignItems={{ sm: "center" }}
                    justifyContent="space-between"
                  >
                    <Box>
                      <Typography fontWeight={650}>
                        Xray {entry.selector}
                      </Typography>
                      <Typography variant="body2" color="text.secondary">
                        {mib(entry.bytes)}
                      </Typography>
                    </Box>
                    {entry.protected ? (
                      <Chip
                        label="Защищена"
                        color="success"
                        variant="outlined"
                      />
                    ) : (
                      <Button
                        color="error"
                        startIcon={<DeleteOutlineRounded />}
                        disabled={busy}
                        onClick={() => setRemove(entry)}
                      >
                        Удалить {entry.selector}
                      </Button>
                    )}
                  </Stack>
                  {entry.protected && (
                    <Typography variant="body2" color="text.secondary" mt={1}>
                      {[...new Set(entry.reasons)]
                        .map((r) => reasons[r] || r)
                        .join(" · ")}
                    </Typography>
                  )}
                </Box>
              ))}
            </>
          )}
        </Stack>
      </AccordionDetails>
      <Dialog
        open={!!remove}
        onClose={() => setRemove(undefined)}
        maxWidth="xs"
        fullWidth
      >
        <DialogTitle>Удалить загруженную сборку?</DialogTitle>
        <DialogContent>
          Xray {remove?.selector} будет удалён с {worker}. Освободится{" "}
          {mib(remove?.bytes || 0)}. При следующем назначении потребуется
          повторная загрузка.
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setRemove(undefined)}>Отмена</Button>
          <Button
            color="error"
            variant="contained"
            onClick={() => void run("remove", remove?.selector)}
          >
            Удалить сборку
          </Button>
        </DialogActions>
      </Dialog>
    </Accordion>
  );
}
