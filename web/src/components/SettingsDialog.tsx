import { Link } from "react-router";
import { ColorPicker } from "@/components/ColorPicker";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { locales, useI18n, type Locale } from "@/i18n/context";
import type { Accent, Theme } from "@/lib/theme";

const categories = ["appearance", "language", "interface", "system"] as const;

export function SettingsPanels({
  theme,
  setTheme,
  accent,
  setAccent,
  githubLink,
  setGithubLink,
  mobile,
  onNavigate,
}: {
  theme: Theme;
  setTheme: (value: Theme) => void;
  accent: Accent;
  setAccent: (value: Accent) => void;
  githubLink: boolean;
  setGithubLink: (value: boolean) => void;
  mobile: boolean;
  onNavigate: () => void;
}) {
  const { t, locale, setLocale } = useI18n();
  return (
    <Tabs
      defaultValue="appearance"
      orientation={mobile ? "horizontal" : "vertical"}
      className="settings-tabs"
    >
      <TabsList
        variant="line"
        activateOnFocus
        aria-label={t.settingsTabsLabel}
        className="settings-tablist"
      >
        {categories.map((value) => (
          <TabsTrigger
            key={value}
            value={value}
            className="text-foreground/80 transition-none group-data-vertical/tabs:px-2.5 group-data-vertical/tabs:after:hidden group-data-vertical/tabs:data-active:bg-muted! group-data-vertical/tabs:data-active:font-semibold dark:text-foreground/80"
          >
            {t.settingsTabs[value]}
          </TabsTrigger>
        ))}
      </TabsList>
      <TabsContent value="appearance" className="settings-panel">
        <section className="settings-section" aria-labelledby="appearance-title">
          <h2 id="appearance-title">{t.theme}</h2>
          <p>{t.themeHint}</p>
          <ToggleGroup
            aria-label={t.theme}
            value={[theme]}
            onValueChange={(values) => {
              if (values[0]) setTheme(values[0] as Theme);
            }}
            variant="outline"
            spacing={0}
          >
            {(["system", "light", "dark"] as const).map((value) => (
              <ToggleGroupItem key={value} value={value}>
                {t.themeLabels[value]}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
        </section>
        <section className="settings-section" aria-labelledby="accent-title">
          <h2 id="accent-title">{t.accentTitle}</h2>
          <p>{t.accentHint}</p>
          <ColorPicker label={t.accentTitle} value={accent} onChange={setAccent} />
          <p className="accent-current" aria-live="polite">
            {t.accentLabels[accent]}
          </p>
        </section>
      </TabsContent>
      <TabsContent value="language" className="settings-panel">
        <section className="settings-section" aria-labelledby="language-title">
          <h2 id="language-title">{t.languageTitle}</h2>
          <p>{t.languageHint}</p>
          <ToggleGroup
            aria-label={t.languageTitle}
            value={[locale]}
            onValueChange={(values) => {
              if (values[0]) setLocale(values[0] as Locale);
            }}
            variant="outline"
            spacing={0}
          >
            {locales.map((value) => (
              <ToggleGroupItem key={value} value={value}>
                {t.languageNames[value]}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
        </section>
      </TabsContent>
      <TabsContent value="interface" className="settings-panel">
        <section className="settings-section" aria-labelledby="github-title">
          <div className="flex items-center justify-between gap-4">
            <h2 id="github-title">{t.githubLinkTitle}</h2>
            <Switch
              checked={githubLink}
              onCheckedChange={setGithubLink}
              aria-labelledby="github-title"
              aria-describedby="github-hint"
            />
          </div>
          <p id="github-hint">{t.githubLinkHint}</p>
        </section>
      </TabsContent>
      <TabsContent value="system" className="settings-panel">
        <section className="settings-section" aria-labelledby="diagnostics-link-title">
          <h2 id="diagnostics-link-title">{t.diagnosticsTitle}</h2>
          <p>{t.diag.diagnosticsLinkHint}</p>
          <Button variant="outline" render={<Link to="/diagnostics" onClick={onNavigate} />}>
            {t.diag.openDiagnostics}
          </Button>
        </section>
      </TabsContent>
    </Tabs>
  );
}
