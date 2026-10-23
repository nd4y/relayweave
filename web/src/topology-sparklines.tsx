import { Box, Tooltip, Typography, useTheme } from "@mui/material";
import type { Row } from "./topology";
import { aggregateHistory, sparkPath } from "./topology-series";
const value = (v: number | null | undefined) =>
  v == null
    ? "нет данных"
    : v.toLocaleString("ru-RU", { maximumFractionDigits: 1 });
export function RouteSparklines({
  members = [],
  direction,
  mobile,
}: {
  members?: Row[];
  direction: string;
  mobile: boolean;
}) {
  const theme = useTheme();
  const points = aggregateHistory(members),
    last = points[points.length - 1];
  const hasMetrics = members.length > 0;
  const keys =
    direction === "up"
      ? ["up" as const]
      : direction === "down"
        ? ["down" as const]
        : ["up" as const, "down" as const];
  const colors = {
    up: theme.palette.primary.main,
    down: theme.palette.success.main,
    rtt: theme.palette.warning.main,
  };
  return (
    <Box
      data-testid="route-sparklines"
      sx={{
        minWidth: 0,
        display: "grid",
        gridTemplateColumns: mobile ? "1fr 1fr" : "1fr",
        columnGap: 1,
        "& .MuiTypography-body2": {
          fontSize: "12px !important",
          lineHeight: "18px !important",
        },
      }}
    >
      {(["speed", "rtt"] as const).map((kind) => {
        const lines = kind === "speed" ? keys : ["rtt" as const];
        const maximum = Math.max(
          0.001,
          ...points.flatMap((p) => lines.map((k) => p[k] ?? 0)),
        );
        const measured = points.some((p) => lines.some((k) => p[k] !== null));
        const label = kind === "speed" ? "Скорость" : "RTT";
        const help = !hasMetrics
          ? "Для этого узла нет отдельных измерений. Настроенная связь не является измеренным потоком."
          : kind === "speed"
            ? "Наблюдаемая скорость по приросту счётчиков. Сумма профилей карточки; окно сглаживания выбрано в фильтрах. Пропуски и сбросы счётчиков разрывают линию."
            : "Оценка ядра: сглаженное время открытия соединения адаптером. Может отражать локальный этап; это не сквозная задержка до сайта и не ping клиента. В кадрах показано состояние оценки, а не новая проба каждую секунду. Для группы - медиана доступных RTT; отсутствующие значения исключены.";
        return (
          <Tooltip key={kind} title={help} describeChild placement="top">
            <Box data-testid={`spark-${kind}`}>
              <Typography
                variant="body2"
                component="div"
                sx={{
                  display: "flex",
                  flexDirection: mobile ? "column" : "row",
                  justifyContent: "space-between",
                  gap: 0.5,
                  fontVariantNumeric: "tabular-nums",
                }}
              >
                <span>{label}</span>
                <span>
                  {!hasMetrics ? (
                    "Не измеряется"
                  ) : kind === "speed" ? (
                    <>
                      {direction !== "down" && (
                        <Box component="span" sx={{ color: colors.up }}>
                          ↑ {value(last?.up)}{" "}
                        </Box>
                      )}
                      {direction !== "up" && (
                        <Box component="span" sx={{ color: colors.down }}>
                          ↓ {value(last?.down)}{" "}
                        </Box>
                      )}
                      Мбит/с
                    </>
                  ) : (
                    `${value(last?.rtt)} мс${members.length > 1 ? " · медиана" : ""}`
                  )}
                </span>
              </Typography>
              <svg
                role="img"
                aria-label={`${label}: последние 60 секунд`}
                viewBox="0 0 256 46"
                width="100%"
                height="25"
                preserveAspectRatio="none"
                style={{ display: "block", overflow: "visible" }}
              >
                <title>{help}</title>
                <path
                  d="M2,43 H254"
                  stroke={theme.palette.divider}
                  fill="none"
                />
                {measured ? (
                  lines.map((k) => (
                    <g key={k}>
                      <path
                        data-series={k}
                        d={sparkPath(points, k, maximum, last?.at || 0)}
                        stroke={colors[k]}
                        strokeWidth="2"
                        fill="none"
                        strokeLinecap="round"
                        strokeLinejoin="round"
                      />
                      {points
                        .filter((p) => p[k] !== null)
                        .map((p) => (
                          <circle
                            key={p.at}
                            cx={
                              2 +
                              Math.max(
                                0,
                                Math.min(
                                  1,
                                  (p.at - ((last?.at || 0) - 60000)) / 60000,
                                ),
                              ) *
                                252
                            }
                            cy={42 - Math.min(1, p[k]! / maximum) * 38}
                            r="1.6"
                            fill={colors[k]}
                          />
                        ))}
                    </g>
                  ))
                ) : (
                  <text
                    x="128"
                    y="27"
                    textAnchor="middle"
                    fill={theme.palette.text.secondary}
                    fontSize="14"
                  >
                    {hasMetrics
                      ? "Ожидание измерений"
                      : "Нет отдельной телеметрии"}
                  </text>
                )}
              </svg>
            </Box>
          </Tooltip>
        );
      })}
      <Typography
        variant="caption"
        color="text.secondary"
        sx={{ gridColumn: "1 / -1", fontSize: 11, lineHeight: "16px" }}
      >
        60 с · с открытия карты
      </Typography>
    </Box>
  );
}
