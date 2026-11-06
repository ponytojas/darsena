import { useEffect, useState } from "react";
export type Theme = "system" | "light" | "dark";
export function useTheme() {
  const [theme, setTheme] = useState<Theme>(() => {
    try {
      const value = localStorage.getItem("darsena.theme");
      return value === "light" || value === "dark" ? value : "system";
    } catch {
      return "system";
    }
  });
  useEffect(() => {
    const media = matchMedia("(prefers-color-scheme: dark)");
    const apply = () => {
      const dark = theme === "dark" || (theme === "system" && media.matches);
      document.documentElement.classList.toggle("dark", dark);
      document.documentElement.style.colorScheme = dark ? "dark" : "light";
    };
    apply();
    try {
      localStorage.setItem("darsena.theme", theme);
    } catch {
      /* Optional preference storage. */
    }
    media.addEventListener("change", apply);
    return () => media.removeEventListener("change", apply);
  }, [theme]);
  return [theme, setTheme] as const;
}

export const accents = ["blue", "cyan", "teal", "green", "lime", "orange", "rust", "red", "rose", "violet", "indigo", "slate"] as const;
export type Accent = (typeof accents)[number];
export function useAccent() {
  const [accent, setAccent] = useState<Accent>(() => {
    try { const saved = localStorage.getItem("darsena.accent"); return accents.find(value => value === saved) ?? "blue"; } catch { return "blue"; }
  });
  useEffect(() => {
    document.documentElement.dataset.accent = accent;
    try { localStorage.setItem("darsena.accent", accent); } catch { /* Optional storage. */ }
  }, [accent]);
  return [accent, setAccent] as const;
}

export function useGithubLink() {
  const [show, setShow] = useState(() => {
    try { return localStorage.getItem("darsena.githubLink") !== "false"; } catch { return true; }
  });
  useEffect(() => {
    try { localStorage.setItem("darsena.githubLink", String(show)); } catch { /* Optional storage. */ }
  }, [show]);
  return [show, setShow] as const;
}
