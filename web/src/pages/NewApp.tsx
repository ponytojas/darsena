import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { ArrowLeft, FlaskConical, Package } from "lucide-react";
import { Button } from "@/components/ui/button";
import { PageTitle } from "@/components/shared";
import { useCapabilities } from "@/lib/capabilities";
import { useI18n } from "@/i18n/context";
import { NewDefinition } from "@/features/definitions/NewDefinition";
import NewApplication from "@/pages/NewApplication";

type Kind = "example" | "definition";

/**
 * Unified creation. Examples exist only in demo; definitions need a ready worker.
 * The chosen kind lives in `?kind=` so back and reload keep it.
 */
export default function NewApp() {
  const { t } = useI18n();
  const f = t.newFlow;
  const capabilities = useCapabilities();
  const [params, setParams] = useSearchParams();
  const [picked, setPicked] = useState<Kind | "">("");
  const demo = capabilities.data?.mode === "demo";
  const both = demo && capabilities.workerReady;
  const requested = params.get("kind");

  if (!demo)
    return (
      <div className="page">
        <PageTitle title={t.newApp} description={t.definitions.newDescription} />
        <NewDefinition />
      </div>
    );

  const kind: Kind | "" = both ? (requested === "example" || requested === "definition" ? requested : "") : "example";

  if (kind === "example")
    return (
      <NewApplication
        lead={
          both ? (
            <Button variant="ghost" size="sm" onClick={() => setParams({})} className="-ml-2 mb-2">
              <ArrowLeft data-icon="inline-start" />
              {f.changeType}
            </Button>
          ) : (
            <p className="pb-5 text-sm text-muted-foreground">
              {f.definitionNeedsWorker}{" "}
              <Button variant="link" className="h-auto p-0" render={<Link to="/diagnostics" />}>
                {f.openDiagnostics}
              </Button>
            </p>
          )
        }
      />
    );

  if (kind === "definition")
    return (
      <div className="page">
        <Button variant="ghost" size="sm" onClick={() => setParams({})} className="-ml-2 mb-2">
          <ArrowLeft data-icon="inline-start" />
          {f.changeType}
        </Button>
        <PageTitle title={t.newApp} />
        <NewDefinition />
      </div>
    );

  const options: { value: Kind; title: string; description: string; icon: typeof Package }[] = [
    { value: "example", title: f.exampleTitle, description: f.exampleDescription, icon: FlaskConical },
    { value: "definition", title: f.definitionTitle, description: f.definitionDescription, icon: Package },
  ];
  return (
    <div className="page">
      <PageTitle title={t.newApp} description={f.chooseDescription} />
      <form
        className="grid max-w-2xl gap-4"
        onSubmit={(event) => {
          event.preventDefault();
          if (picked) setParams({ kind: picked });
        }}
      >
        <fieldset className="grid gap-3 sm:grid-cols-2">
          <legend className="mb-3 text-sm font-medium">{f.kindLegend}</legend>
          {options.map(({ value, title, description, icon: Icon }) => (
            <label
              key={value}
              className="flex cursor-pointer flex-col gap-2 rounded-lg border bg-card p-4 transition-colors hover:bg-accent has-[:checked]:border-primary has-[:checked]:ring-2 has-[:checked]:ring-primary/30 has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring"
            >
              <input
                type="radio"
                name="kind"
                value={value}
                checked={picked === value}
                onChange={() => setPicked(value)}
                className="sr-only"
              />
              <Icon aria-hidden="true" className="size-5 text-muted-foreground" />
              <span className="font-medium">{title}</span>
              <span className="text-sm text-muted-foreground">{description}</span>
            </label>
          ))}
        </fieldset>
        <div className="flex gap-2 pt-2">
          <Button variant="ghost" render={<Link to="/" />}>{t.cancel}</Button>
          <Button type="submit" disabled={!picked}>{t.next}</Button>
        </div>
      </form>
    </div>
  );
}
