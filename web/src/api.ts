export type Item = {
  id: string;
  kind: string;
  name: string;
  version: number;
  updated_at: string;
  data: Record<string, any>;
};
let csrf = "";
function idempotencyKey(): string {
  // getRandomValues is also available on ordinary LAN HTTP origins.
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (value) => value.toString(16).padStart(2, "0"));
  return [hex.slice(0, 4), hex.slice(4, 6), hex.slice(6, 8), hex.slice(8, 10), hex.slice(10)].map((part) => part.join("")).join("-");
}
export class APIError extends Error {
  status: number;
  constructor(code: string, status: number) {
    super(code);
    this.status = status;
  }
}
export async function api<T = any>(
  path: string,
  method = "GET",
  body?: unknown,
  version?: number,
): Promise<T> {
  const headers: Record<string, string> = {};
  if (method !== "GET") {
    headers["X-CSRF-Token"] = csrf;
    headers["Idempotency-Key"] = idempotencyKey();
  }
  if (version !== undefined) headers["If-Match"] = String(version);
  let data: BodyInit | undefined;
  if (body instanceof FormData) {
    data = body;
  } else if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    data = JSON.stringify(body);
  }
  const controller = new AbortController();
  const timeout = setTimeout(
    () => controller.abort(),
    path.startsWith("backup") || path.startsWith("support") ? 300000 : 15000,
  );
  let response: Response;
  try {
    response = await fetch("/api/v1/" + path, {
      method,
      headers,
      body: data,
      credentials: "same-origin",
      signal: controller.signal,
    });
  } catch (e) {
    if (controller.signal.aborted) throw new Error("request_timeout");
    throw e;
  } finally {
    clearTimeout(timeout);
  }
  const value = await response.json().catch(() => ({}));
  if (response.status === 401 && path !== "auth/login")
    window.dispatchEvent(new Event("relayweave-session-expired"));
  if (!response.ok)
    throw new APIError(
      value.error?.code || `HTTP ${response.status}`,
      response.status,
    );
  if (value.csrf) csrf = value.csrf;
  return value as T;
}
export async function all(kind: string): Promise<Item[]> {
  let out: Item[] = [];
  for (let offset = 0; offset < 20000; offset += 1000) {
    const result = await api<{ items: Item[]; total: number }>(
      `${kind}?limit=1000&offset=${offset}`,
    );
    out = out.concat(result.items);
    if (out.length >= result.total) break;
  }
  return out;
}
export async function downloadBackup(password: string) {
  const response = await fetch("/api/v1/backup", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
    body: JSON.stringify({ password }),
  });
  if (!response.ok) throw new Error("backup_export_failed");
  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `relayweave-${new Date().toISOString().slice(0, 10)}.rwb`;
  a.click();
  URL.revokeObjectURL(url);
}
export const errors: Record<string, string> = {
  artifact_cache_in_use: "Сборка используется активными соединениями или нужна для восстановления. Удаление запрещено.",
  artifact_cache_not_found: "Сборка уже отсутствует на этом воркере. Обновите кэш.",
  artifact_cache_apply_in_progress: "Сейчас применяется конфигурация. Дождитесь завершения и повторите действие.",
  artifact_cache_recovery_unreadable: "Не удалось проверить резервную конфигурацию. Обслуживание кэша заблокировано для сохранения возможности восстановления.",
  artifact_identity_invalid: "Укажите версию Xray, короткое имя варианта и поддерживаемую архитектуру.",
  artifact_repository_not_allowed: "Репозиторий отсутствует в XRAY_ALLOWED_REPOSITORIES. Добавьте доверенный источник в конфигурацию сервиса.",
  artifact_release_url_invalid: "URL должен указывать на ZIP в GitHub Releases выбранного репозитория и тега.",
  artifact_digest_invalid: "SHA-256 должен содержать 64 шестнадцатеричных символа.",
  artifact_metadata_invalid: "Проверьте лицензию и описание возможностей сборки.",
  artifact_identity_immutable: "Для этого идентификатора уже закреплён другой файл. Используйте новое имя варианта.",
  core_variant_not_approved: "Сначала одобрите вариант сборки, указав источник и SHA-256.",
  acl_cidr_invalid: "Проверьте разрешённые сети. Используйте CIDR, например 192.168.1.0/24.",
  artifact_binary_digest_missing:
    "Для этой сборки не закреплён хеш бинарника. Повторите проверку и установку ядра.",
  artifact_binary_digest_mismatch:
    "Бинарник ядра отличается от проверенной сборки. Запуск заблокирован.",
  core_binary_digest_mismatch:
    "Проверка целостности ядра не пройдена. Запуск заблокирован.",
  artifact_archive_integrity_failed:
    "Архив ядра повреждён или имеет неподходящую архитектуру.",
  artifact_download_failed:
    "Не удалось скачать ядро из разрешённого источника.",
  command_wrong_worker: "Команда адресована другому воркеру и не выполнена.",
  profile_source_expired:
    "Срок годности подписки или её квота истекли. Обновите источник перед проверкой.",
  profile_source_unavailable:
    "Профиль удалён из источника или подписка отключена.",
  request_timeout:
    "Сервер не ответил вовремя. Проверьте связь и повторите действие.",
  ambiguous_identity_manual_override_held:
    "Параметры профиля неоднозначно изменились. Ручное исключение сохранено: проверьте новый профиль перед включением.",
  incorrect_password: "Неверный пароль",
  login_rate_limit: "Слишком много попыток. Подождите минуту.",
  revision_conflict:
    "Объект изменён в другой вкладке. Обновите данные и повторите изменение.",
  csrf_rejected: "Сессия устарела. Обновите страницу.",
  source_address_blocked:
    "Адрес запрещён. Для внутреннего сервиса явно разрешите частные адреса.",
  pool_has_no_eligible_profiles:
    "В пуле нет доступных профилей. Обновите подписки и проверьте фильтры.",
  enabled_listener_required: "Добавьте хотя бы одну включённую точку входа.",
  worker_hot_path_budget_exceeded:
    "Сумма активных путей и резерва всех политик превышает 16.",
  worker_validation_failed:
    "Воркер отклонил конфигурацию. Проверьте настройки, доступность профилей и выбранные ядра.",
  core_version_not_installed_or_approved:
    "Сначала добавьте нужную версию Xray в разделе «Ядра».",
  operation_failed: "Операция не выполнена. Подробности доступны в журнале.",
  object_in_use_or_changed:
    "Объект используется другими настройками или уже изменён.",
  filter_invalid:
    "Некорректный фильтр. Регулярные выражения используют RE2 без lookahead/lookbehind.",
  speed_url_required: "Задайте URL тестового файла в политике.",
  probe_budget_exhausted: "Суточный бюджет активных тестов исчерпан.",
  backup_password_or_integrity:
    "Неверный пароль резервной копии или повреждённый файл.",
  source_settings_invalid:
    "Проверьте URL, интервалы обновления и срок годности подписки.",
  subscription_parse_failed:
    "Источник вернул неподдерживаемое или повреждённое содержимое.",
  source_fetch_failed:
    "Не удалось получить подписку. Проверьте адрес, заголовки и доступность источника.",
  source_dns_failed: "Не удалось разрешить адрес источника.",
  source_header_invalid: "Проверьте названия и значения заголовков запроса.",
  review_required:
    "Изменился состав или параметры подписки. Просмотрите разницу перед принятием.",
  source_refresh_in_progress:
    "Подписка уже обновляется. Дождитесь завершения текущей операции.",
  catalogue_budget_exceeded:
    "Достигнут лимит каталога. Удалите ненужные подписки или профили из источника.",
  artifact_cache_budget_exceeded:
    "Кеш ядер заполнен. Удалите неиспользуемые версии.",
  artifact_disk_reserve_required:
    "Для безопасной установки ядра требуется не менее 256 МиБ свободного места.",
  engine_budget_exhausted:
    "Все места для процессов заняты. Дождитесь завершения дренирования старых соединений.",
  policy_has_no_healthy_path:
    "У политики нет рабочего маршрута. Проверьте источник и назначенную версию ядра.",
  proxy_password_max_72_bytes:
    "Пароль прокси должен занимать не более 72 байт UTF-8.",
  profile_referenced_by_pool:
    "Профили этой подписки используются ручным списком или исключениями пула.",
  outbound_dependency_requires_review:
    "Профиль использует цепочку outbound, которая пока не поддерживается адаптером.",
  subscription_file_reference_rejected:
    "В подписке есть ссылка на локальный файл. Такой профиль не запускается.",
  protocol_requires_another_engine:
    "Для этого протокола нужен другой движок. Адаптер Xray его не поддерживает.",
  session_expired: "Сессия истекла. Войдите снова.",
};
export function message(e: unknown) {
  const text = e instanceof Error ? e.message : String(e);
  if (text.startsWith("source_http_"))
    return `Источник вернул HTTP ${text.slice(12)}. Проверьте ссылку и заголовки.`;
  return errors[text] || text.replaceAll("_", " ");
}
export function ago(value?: string | number) {
  if (!value || String(value).startsWith("0001")) return "ещё не было";
  const t = typeof value === "number" ? value * 1000 : Date.parse(value);
  const sec = Math.max(0, (Date.now() - t) / 1000);
  return sec < 60
    ? "только что"
    : sec < 3600
      ? `${Math.floor(sec / 60)} мин назад`
      : sec < 86400
        ? `${Math.floor(sec / 3600)} ч назад`
        : `${Math.floor(sec / 86400)} дн назад`;
}
