import { useEffect, useMemo, useState, type ReactNode } from "react";
import {
  I18nContext,
  dictionaries,
  readLocale,
  storageKey,
  type Locale,
} from "./context";

export function I18nProvider({ children }: { children: ReactNode }) {
  const [locale, setLocale] = useState<Locale>(readLocale);
  useEffect(() => {
    document.documentElement.lang = locale;
    try {
      localStorage.setItem(storageKey, locale);
    } catch {
      /* Optional preference storage. */
    }
  }, [locale]);
  const value = useMemo(
    () => ({ t: dictionaries[locale], locale, setLocale }),
    [locale],
  );
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}
