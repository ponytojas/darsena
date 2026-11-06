import { createContext, useContext } from "react";
import { en } from "./en";
import { es, type Messages } from "./es";

export type Locale = "es" | "en";
export const locales: readonly Locale[] = ["es", "en"];
export const dictionaries: Record<Locale, Messages> = { es, en };
export const storageKey = "darsena.locale";

export function readLocale(): Locale {
  try {
    const value = localStorage.getItem(storageKey);
    return locales.find((locale) => locale === value) ?? "es";
  } catch {
    return "es";
  }
}

export type I18n = { t: Messages; locale: Locale; setLocale: (locale: Locale) => void };
export const I18nContext = createContext<I18n | null>(null);

export function useI18n(): I18n {
  const value = useContext(I18nContext);
  if (!value) throw new Error("useI18n must be used inside I18nProvider");
  return value;
}
