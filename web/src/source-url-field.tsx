import { useEffect, useRef, useState } from "react";
import {
  CircularProgress,
  IconButton,
  InputAdornment,
  TextField,
  Tooltip,
} from "@mui/material";
import VisibilityRounded from "@mui/icons-material/VisibilityRounded";
import VisibilityOffRounded from "@mui/icons-material/VisibilityOffRounded";
import { api } from "./api";

type Props = {
  sourceId?: string;
  sourceVersion?: number;
  value?: string;
  onChange?: (value: string) => void;
  hint?: string;
  configured?: boolean;
  required?: boolean;
  helperText?: string;
  readOnly?: boolean;
};

export function SourceURLField({
  sourceId,
  sourceVersion,
  value = "",
  onChange,
  hint,
  configured,
  required,
  helperText,
  readOnly = false,
}: Props) {
  const [shown, setShown] = useState(false);
  const [saved, setSaved] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(false);
  const epoch = useRef(0);
  const identity = useRef({ sourceId, sourceVersion });
  // Invalidate immediately during render so a new object cannot render a secret
  // from the previous source before effects run.
  const sameSource =
    identity.current.sourceId === sourceId &&
    identity.current.sourceVersion === sourceVersion;

  const hide = () => {
    epoch.current++;
    setShown(false);
    setSaved("");
    setLoading(false);
    setError(false);
  };
  useEffect(() => {
    identity.current = { sourceId, sourceVersion };
    hide();
    return () => {
      epoch.current++;
    };
  }, [sourceId, sourceVersion]);
  useEffect(() => {
    const listener = () => {
      if (document.hidden) hide();
    };
    document.addEventListener("visibilitychange", listener);
    return () => document.removeEventListener("visibilitychange", listener);
  }, []);
  useEffect(() => {
    if (!shown) return;
    const timer = window.setTimeout(hide, 60_000);
    return () => window.clearTimeout(timer);
  }, [shown]);

  const reveal = async () => {
    if (shown) {
      hide();
      return;
    }
    setError(false);
    if (value) {
      setShown(true);
      return;
    }
    if (!sourceId || !configured) return;
    const request = ++epoch.current;
    setLoading(true);
    try {
      const result = await api<{ url: string }>(
        `sources/${encodeURIComponent(sourceId)}/reveal-url`,
        "POST",
        {},
      );
      if (request !== epoch.current) return;
      if (typeof result.url !== "string" || !result.url)
        throw new Error("unavailable");
      setSaved(result.url);
      setShown(true);
    } catch {
      if (request === epoch.current) setError(true);
    } finally {
      if (request === epoch.current) setLoading(false);
    }
  };
  const visible = sameSource && shown;
  const label = visible ? "Скрыть адрес подписки" : "Показать адрес подписки";
  return (
    <TextField
      fullWidth
      label="URL подписки"
      required={required}
      autoComplete="off"
      type={readOnly || visible ? "text" : "password"}
      value={
        readOnly
          ? visible
            ? saved
            : hint || "Адрес скрыт"
          : value || (visible ? saved : "")
      }
      placeholder={hint || "https://…"}
      onChange={(event) => {
        // A late saved-URL response must not replace a value entered meanwhile.
        epoch.current++;
        setLoading(false);
        setSaved("");
        onChange?.(event.target.value);
      }}
      error={error}
      helperText={
        error ? "Не удалось показать адрес. Повторите попытку." : helperText
      }
      slotProps={{
        htmlInput: { spellCheck: false, readOnly, maxLength: 8192 },
        input: {
          endAdornment: (
            <InputAdornment position="end">
              <Tooltip title={label}>
                <span>
                  <IconButton
                    aria-label={label}
                    aria-pressed={visible}
                    edge="end"
                    disabled={loading || (!value && !(sourceId && configured))}
                    onMouseDown={(event) => event.preventDefault()}
                    onClick={reveal}
                  >
                    {loading ? (
                      <CircularProgress size={20} />
                    ) : visible ? (
                      <VisibilityOffRounded />
                    ) : (
                      <VisibilityRounded />
                    )}
                  </IconButton>
                </span>
              </Tooltip>
            </InputAdornment>
          ),
        },
      }}
    />
  );
}
