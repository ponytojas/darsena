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
