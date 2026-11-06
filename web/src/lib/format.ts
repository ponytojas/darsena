import type { Locale } from "@/i18n/context";

export const formatDateTime = (date: string, locale: Locale) =>
  new Intl.DateTimeFormat(locale, {
    day: "2-digit",
    month: "short",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(date));

export const formatTime = (date: string, locale: Locale) =>
  new Date(date).toLocaleTimeString(locale);

const units: [Intl.RelativeTimeFormatUnit, number][] = [
  ["day", 86_400_000],
  ["hour", 3_600_000],
  ["minute", 60_000],
];

export function formatRelative(date: string, locale: Locale, now = Date.now()) {
  const diff = new Date(date).getTime() - now;
  const format = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
  for (const [unit, size] of units)
    if (Math.abs(diff) >= size) return format.format(Math.round(diff / size), unit);
  return format.format(Math.round(diff / 1000), "second");
}
