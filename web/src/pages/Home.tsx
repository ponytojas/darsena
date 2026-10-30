import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router";
import { ArrowRight, Search } from "lucide-react";
import { api } from "@/api/client";
import { es as t } from "@/i18n/es";
import {
  PageTitle,
  NewAppButton,
  Failure,
  Loading,
  Blank,
  ApplicationRows,
  OperationRows,
} from "@/components/shared";
import { Input } from "@/components/ui/input";
export default function Home() {
  const operations = useQuery({
    queryKey: ["operations"],
    queryFn: api.operations,
    refetchInterval: (query) =>
      query.state.data?.some(
        (op) => op.status === "queued" || op.status === "running",
      )
        ? 2000
        : false,
  });
  const active = operations.data?.some(
    (op) => op.status === "queued" || op.status === "running",
  );
  const apps = useQuery({
    queryKey: ["applications"],
    queryFn: api.apps,
    refetchInterval: active ? 2000 : false,
  });
  const [search, setSearch] = useSearchParams();
  const term = search.get("q") || "";
  const filtered =
    apps.data?.filter((a) =>
      `${a.name} ${a.repository}`.toLowerCase().includes(term.toLowerCase()),
    ) || [];
  return (
    <div className="page home-page">
      <PageTitle
        eyebrow={t.overview}
        title={t.applications}
        description={t.homeDescription}
        action={<NewAppButton />}
      />
      <div className="summary-strip">
        <div>
          <span>{t.total}</span>
          <strong>{apps.data?.length ?? "—"}</strong>
        </div>
        <div>
          <span>{t.healthy}</span>
          <strong>
            {apps.data?.filter((a) => a.status === "healthy").length ?? "—"}
            <i className="health-dot" />
          </strong>
        </div>
        <div>
          <span>{t.active}</span>
          <strong>
            {operations.data?.filter(
              (o) => o.status === "queued" || o.status === "running",
            ).length ?? "—"}
          </strong>
        </div>
        <div>
          <span>{t.attention}</span>
          <strong>
            {apps.data?.filter((a) => a.status === "failed").length ?? "—"}
          </strong>
        </div>
      </div>
      <section aria-label={t.applications} className="resource-section">
        <div className="list-toolbar">
          <label className="search-control">
            <Search aria-hidden="true" />
            <span className="sr-only">{t.search}</span>
            <Input
              type="search"
              placeholder={t.searchPlaceholder}
              value={term}
              onChange={(e) =>
                setSearch(e.target.value ? { q: e.target.value } : {}, {
                  replace: true,
                })
              }
            />
          </label>
          <span className="list-count">
            {filtered.length} {t.applications.toLowerCase()}
          </span>
        </div>
        {apps.isPending ? (
          <Loading />
        ) : apps.isError ? (
          <Failure error={apps.error} retry={() => void apps.refetch()} />
        ) : filtered.length ? (
          <ApplicationRows apps={filtered} />
        ) : (
          <Blank
            title={term ? t.noMatches : t.noApps}
            description={term ? t.noMatchesDescription : t.noAppsDescription}
            action={!term && <NewAppButton />}
          />
        )}
      </section>
      <section className="recent-section">
        <div className="section-heading">
          <h2>{t.recent}</h2>
          <Link to="/activity">
            {t.allActivity}
            <ArrowRight aria-hidden="true" />
          </Link>
        </div>
        {operations.isError ? (
          <Failure
            error={operations.error}
            retry={() => void operations.refetch()}
          />
        ) : operations.isPending ? (
          <Loading />
        ) : operations.data?.length ? (
          <OperationRows
            operations={operations.data.slice(0, 4)}
            apps={apps.data}
          />
        ) : (
          <Blank title={t.noActivity} description={t.noActivityDescription} />
        )}
      </section>
    </div>
  );
}
